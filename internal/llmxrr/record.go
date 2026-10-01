package llmxrr

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	xrr "hop.top/xrr"
)

// ErrRefused marks a recording refused because the live exchange
// failed: a transport error or a non-2xx status. A failure is never
// written to a cassette: replay would hand it back forever, and since
// record mode only records what is missing, re-recording would never
// retry it.
var ErrRefused = errors.New("llmxrr: refusing to record a failed exchange")

// ErrStoredFailure marks a cassette entry that holds a failed exchange,
// written before failures were refused. Replay will not hand it to foo
// as if the provider had said it; record mode treats it as missing and
// sends the request again.
var ErrStoredFailure = errors.New("llmxrr: cassette holds a failed exchange")

// CheckRecordable returns an ErrRefused error when a live exchange must
// not be recorded: err is the transport error (nil when a response
// came back), status the HTTP status, body the response body. path is
// the request path as the cassette stores it (see RecordedURL). The
// message names the method, path, status, and the start of the body,
// with credential shapes and the recording keys masked.
func CheckRecordable(method, path string, status int, body []byte, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s %s: transport error: %s; nothing was written, fix the cause and record again",
			ErrRefused, method, path, Redact(transportCause(err)))
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("%w: %s %s: status %d %s: %s; nothing was written, fix the cause and record again",
			ErrRefused, method, path, status, http.StatusText(status), snippet(body))
	}
	return nil
}

// transportCause drops the *url.Error wrapper, whose URL can carry a
// key= query param.
func transportCause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// snippet is the start of a response body on one line, masked.
func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" {
		return "(empty body)"
	}
	const max = 300
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return Redact(s)
}

// redactPatterns mask credential shapes in a message; the replacement
// keeps the prefix so the reader still sees which kind of key it was.
var redactPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{4,}`), "sk-<redacted>"},
	{regexp.MustCompile(`AIza[A-Za-z0-9_-]{8,}`), "AIza<redacted>"},
	{regexp.MustCompile(`([?&])key=[^&\s"']*`), "${1}<redacted>"},
	{regexp.MustCompile(`(?i)(bearer\s+)[^\s"']+`), "${1}<redacted>"},
}

// Redact masks credential shapes and the provider keys set in the
// environment (RecordingKeys) in s, for error messages.
func Redact(s string) string {
	for _, k := range RecordingKeys() {
		if len(k) >= 8 {
			s = strings.ReplaceAll(s, k, "<redacted>")
		}
	}
	for _, p := range redactPatterns {
		s = p.re.ReplaceAllString(s, p.with)
	}
	return s
}

// CheckNoFailures fails when an http cassette under dir holds a failed
// exchange: an error envelope or a non-2xx status. Other adapters are
// skipped; a recorded exec failure can be the point of a test.
func CheckNoFailures(dir string) error {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".resp.yaml") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var env struct {
			Adapter string `yaml:"adapter"`
			Error   string `yaml:"error"`
			Payload struct {
				Status int `yaml:"status"`
			} `yaml:"payload"`
		}
		if err := yaml.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("llmxrr: %s: %w", path, err)
		}
		switch {
		case env.Adapter != "http":
		case env.Error != "":
			found = append(found, fmt.Sprintf("%s: recorded error: %s", path, Redact(env.Error)))
		case env.Payload.Status < 200 || env.Payload.Status > 299:
			found = append(found, fmt.Sprintf("%s: status %d", path, env.Payload.Status))
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return fmt.Errorf("%w: delete these and re-record:\n%s", ErrStoredFailure, strings.Join(found, "\n"))
	}
	return nil
}

// modeOf is the session's mode when it reports one (*xrr.FileSession
// does), else "".
func modeOf(s xrr.Session) xrr.Mode {
	if m, ok := s.(interface{ Mode() xrr.Mode }); ok {
		return m.Mode()
	}
	return ""
}

// storedFailure describes a replayed entry that holds a failure: xrr's
// error envelope, or a non-2xx status. Only a replayed response
// (*xrr.RawResponse) qualifies; a live one is judged by CheckRecordable.
func storedFailure(resp xrr.Response, err error) (string, bool) {
	raw, ok := resp.(*xrr.RawResponse)
	if !ok {
		return "", false
	}
	if err != nil {
		return "recorded error: " + Redact(err.Error()), true
	}
	if status, ok := raw.Payload["status"].(int); ok && (status < 200 || status > 299) {
		return fmt.Sprintf("status %d %s", status, http.StatusText(status)), true
	}
	return "", false
}

// voided is the refusal that ended this recording, if any.
func (t *Transport) voided() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refusal
}

// void ends the recording: later misses fail with err and never reach
// the network, so an SDK retrying the refused request spends no live
// calls and the cassette gets no half of a round.
func (t *Transport) void(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refusal == nil {
		t.refusal = err
	}
}
