// Package llmxrr records and replays model-provider HTTP calls through an
// xrr session, so tests run foo's real provider adapters against
// responses a real provider returned.
//
// The seam is http.DefaultTransport: kit's provider adapters (openai-go,
// anthropic-sdk-go, and kit's own Gemini and Ollama clients) all send
// through http.DefaultClient, whose nil Transport resolves to
// http.DefaultTransport at request time. kit offers no per-client HTTP
// option, so a test swaps the process transport for a Transport; the foo
// binary does the same only when built with the xrr tag (see
// xrr_seam.go at the repo root).
//
// Test-only: nothing in a release build imports this package.
package llmxrr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	xrr "hop.top/xrr"
	xhttp "hop.top/xrr/adapters/http"
)

// ElidedToolResult replaces a tool result's content in the recorded,
// fingerprinted request when ElideToolResults is set.
const ElidedToolResult = "<elided: tool result>"

// Subst maps a volatile value (a temp dir, a cwd) to a stable
// placeholder. Recorded requests and responses hold the placeholder;
// a replayed response gets the current run's value back.
type Subst struct {
	Value       string
	Placeholder string
}

// Exchange is one model request as it went over the seam.
type Exchange struct {
	Method string `json:"method"`
	// URL is the request URL with any key= query param removed.
	URL string `json:"url"`
	// Body is the request body foo sent, unnormalized.
	Body json.RawMessage `json:"body"`
	// Canonical is the normalized body the cassette is keyed on.
	Canonical string `json:"canonical"`
	// Fingerprint is the xrr fingerprint of Canonical.
	Fingerprint string `json:"fingerprint"`
	Status      int    `json:"status"`
	// Response is the response body foo received.
	Response json.RawMessage `json:"response"`
	// Live reports that the request reached the network (record mode).
	Live bool `json:"live"`
	// Miss reports that replay found no recording for the request.
	Miss bool `json:"miss"`
}

// Transport is an http.RoundTripper that sends model-provider requests
// through an xrr session and refuses every other request.
type Transport struct {
	Session xrr.Session
	// Replay, when set, is tried first; only a request it has no
	// recording for goes to Session. With a record-mode Session this
	// records just the missing calls and keeps every existing one.
	Replay xrr.Session
	// Next carries record-mode requests to the network.
	Next http.RoundTripper
	// Subst lists volatile values to replace with placeholders.
	Subst []Subst
	// ElideToolResults drops OpenAI-shaped tool result content from
	// the recorded request, keeping tool_call_id. For suites whose
	// tool output depends on the platform (directory order, /etc
	// layout): the fingerprint then covers the call linkage and the
	// prompt, not what a local command printed.
	ElideToolResults bool
	// OnExchange, when set, sees every model request after it
	// completes (or misses).
	OnExchange func(Exchange)

	adapter xhttp.Adapter
	live    atomic.Int64
	seen    atomic.Int64
}

// Live is the number of requests that reached the network.
func (t *Transport) Live() int64 { return t.live.Load() }

// Seen is the number of model requests the transport handled.
func (t *Transport) Seen() int64 { return t.seen.Load() }

// IsModelCall reports whether r is a chat request to a model provider:
// OpenAI-compatible /chat/completions, Anthropic /messages, Gemini
// :generateContent or :streamGenerateContent.
func IsModelCall(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasSuffix(p, "/chat/completions") ||
		strings.HasSuffix(p, "/messages") ||
		strings.HasSuffix(p, ":generateContent") ||
		strings.HasSuffix(p, ":streamGenerateContent")
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !IsModelCall(r) {
		// An upgrade check or catalog fetch would otherwise leave the
		// machine during replay, or land in the cassette on record.
		return nil, fmt.Errorf("llmxrr: refusing %s %s: only model-provider calls go through the cassette", r.Method, r.URL.Redacted())
	}
	t.seen.Add(1)

	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("llmxrr: read request body: %w", err)
		}
		body = b
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	canonical := t.canonical(body)
	req := &xhttp.Request{Method: r.Method, URL: RecordedURL(r.URL), Body: canonical}
	fp, _ := t.adapter.Fingerprint(req)

	ex := Exchange{Method: r.Method, URL: stripKey(r.URL), Body: jsonOrString(body), Canonical: canonical, Fingerprint: fp}
	var resp xrr.Response
	var err error = xrr.ErrCassetteMiss
	if t.Replay != nil {
		resp, err = t.Replay.Record(r.Context(), &t.adapter, req, nil)
	}
	if errors.Is(err, xrr.ErrCassetteMiss) {
		resp, err = t.record(r, req, &ex)
	}
	return t.finish(r, req, fp, ex, resp, err)
}

// record sends req through Session: replayed in replay mode, sent live
// and saved in record mode.
func (t *Transport) record(r *http.Request, req *xhttp.Request, ex *Exchange) (xrr.Response, error) {
	return t.Session.Record(r.Context(), &t.adapter, req, func() (xrr.Response, error) {
		// Only record mode runs this; replay never touches the network.
		t.live.Add(1)
		ex.Live = true
		out, lerr := t.Next.RoundTrip(r)
		if lerr != nil {
			return nil, lerr
		}
		defer out.Body.Close()
		data, rerr := io.ReadAll(out.Body)
		if rerr != nil {
			return nil, rerr
		}
		return &xhttp.Response{
			Status:  out.StatusCode,
			Headers: map[string]string{"Content-Type": out.Header.Get("Content-Type")},
			Body:    t.hide(string(data)),
		}, nil
	})
}

// finish turns a session result into the response foo sees.
func (t *Transport) finish(r *http.Request, req *xhttp.Request, fp string, ex Exchange, resp xrr.Response, err error) (*http.Response, error) {
	if errors.Is(err, xrr.ErrCassetteMiss) {
		ex.Miss = true
		t.observe(ex)
		return nil, fmt.Errorf("llmxrr: no recording for %s %s (fingerprint %s): the request differs from every recorded one; if the change is intended, re-record: %w",
			r.Method, req.URL, fp, err)
	}
	if err != nil {
		t.observe(ex)
		return nil, err
	}

	status, ctype, text := http.StatusOK, "application/json", ""
	switch v := resp.(type) {
	case *xhttp.Response:
		status, text = v.Status, v.Body
		if c := v.Headers["Content-Type"]; c != "" {
			ctype = c
		}
	case *xrr.RawResponse:
		// Replay returns the payload as an untyped map.
		if s, ok := v.Payload["status"].(int); ok {
			status = s
		}
		if s, ok := v.Payload["body"].(string); ok {
			text = s
		}
		if h, ok := v.Payload["headers"].(map[string]any); ok {
			if c, ok := h["Content-Type"].(string); ok && c != "" {
				ctype = c
			}
		}
	}
	text = t.reveal(text)
	ex.Status, ex.Response = status, jsonOrString([]byte(text))
	t.observe(ex)

	h := make(http.Header)
	h.Set("Content-Type", ctype)
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(text)),
		ContentLength: int64(len(text)),
		Request:       r,
	}, nil
}

func (t *Transport) observe(ex Exchange) {
	if t.OnExchange != nil {
		t.OnExchange(ex)
	}
}

// canonical is the body the cassette is keyed on: placeholders for
// volatile values, tool results elided when asked, and JSON re-encoded
// with sorted keys and indentation so the recording diffs cleanly.
func (t *Transport) canonical(body []byte) string {
	s := t.hide(string(body))
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return s
	}
	if t.ElideToolResults {
		elideToolResults(v)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return s
	}
	return buf.String()
}

// elideToolResults blanks the content of OpenAI-shaped tool messages.
func elideToolResults(v any) {
	obj, ok := v.(map[string]any)
	if !ok {
		return
	}
	msgs, _ := obj["messages"].([]any)
	for _, m := range msgs {
		if msg, ok := m.(map[string]any); ok && msg["role"] == "tool" {
			msg["content"] = ElidedToolResult
		}
	}
}

// hide replaces volatile values with their placeholders, longest value
// first so a value nested in another is not split.
func (t *Transport) hide(s string) string {
	subs := append([]Subst(nil), t.Subst...)
	sort.Slice(subs, func(i, j int) bool { return len(subs[i].Value) > len(subs[j].Value) })
	for _, sub := range subs {
		if sub.Value != "" {
			s = strings.ReplaceAll(s, sub.Value, sub.Placeholder)
		}
	}
	return s
}

// reveal puts this run's values back in place of the placeholders.
func (t *Transport) reveal(s string) string {
	for _, sub := range t.Subst {
		if sub.Placeholder != "" {
			s = strings.ReplaceAll(s, sub.Placeholder, sub.Value)
		}
	}
	return s
}

// RecordedURL is the URL a cassette stores: path and query only, key=
// removed. The host is dropped so a recording made against one endpoint
// (a tunnel port, a local server) replays against any other; xrr's http
// fingerprint ignores it anyway.
func RecordedURL(u *url.URL) string {
	c := *u
	c.Scheme, c.Host, c.User = "", "", nil
	return stripKey(&c)
}

// stripKey drops the key= query param Gemini authenticates with.
func stripKey(u *url.URL) string {
	c := *u
	q := c.Query()
	if q.Has("key") {
		q.Del("key")
		c.RawQuery = q.Encode()
	}
	return c.String()
}

func jsonOrString(b []byte) json.RawMessage {
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	s, _ := json.Marshal(string(b))
	return s
}

// Journal returns an OnExchange func appending each exchange as one JSON
// line to path, for a parent process to read what a child sent.
func Journal(path string) func(Exchange) {
	var mu sync.Mutex
	return func(ex Exchange) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "llmxrr: journal:", err)
			return
		}
		defer f.Close()
		line, _ := json.Marshal(ex)
		_, _ = f.Write(append(line, '\n'))
	}
}

// ReadJournal reads the exchanges Journal wrote; a missing file is none.
func ReadJournal(path string) ([]Exchange, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Exchange
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ex Exchange
		if err := json.Unmarshal(line, &ex); err != nil {
			return nil, fmt.Errorf("llmxrr: journal line: %w", err)
		}
		out = append(out, ex)
	}
	return out, nil
}

// secretPatterns are credential shapes a cassette must never hold:
// OpenAI/Anthropic keys, a Gemini key= query param, auth headers.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`[?&]key=`),
	regexp.MustCompile(`AIza[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`(?i)\b(authorization|x-api-key|x-goog-api-key)\b`),
}

// CheckNoSecrets fails when any file under dir holds a credential
// shape or one of the given secret values (the keys used to record).
func CheckNoSecrets(dir string, secrets ...string) error {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, s := range secrets {
			if len(s) >= 8 && bytes.Contains(data, []byte(s)) {
				found = append(found, path+": holds a recording key")
			}
		}
		for _, re := range secretPatterns {
			if loc := re.FindIndex(data); loc != nil {
				found = append(found, fmt.Sprintf("%s: matches %s", path, re))
			}
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
		return fmt.Errorf("llmxrr: secrets in cassettes:\n%s", strings.Join(found, "\n"))
	}
	return nil
}

// RecordingKeys returns the provider keys set in the environment, for
// CheckNoSecrets. Read it before a test overrides them.
func RecordingKeys() []string {
	var keys []string
	for _, k := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "LLM_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			keys = append(keys, v)
		}
	}
	return keys
}
