package e2e

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"hop.top/foo/internal/llmxrr"
)

// The xrr seam in a foo binary (xrr_seam.go) records model calls under
// XRR_MODE=record. These tests drive it against local stand-in
// providers, never a real one: a failed exchange must never become a
// cassette, and a cassette that already holds one must heal on the
// next recording.

// chunkReply is a minimal OpenAI streamed chat reply saying "hello".
const chunkReply = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

// standIn answers every chat request with status: chunkReply on 200,
// an error envelope otherwise. hits counts the requests it saw.
type standIn struct {
	*httptest.Server
	hits atomic.Int64
}

func newStandIn(t *testing.T, status int) *standIn {
	t.Helper()
	s := &standIn{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"stand-in failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunkReply)
	}))
	t.Cleanup(s.Close)
	return s
}

// deadEndpoint is a base URL nothing listens on.
func deadEndpoint(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

type seamRun struct {
	code           int
	stdout, stderr string
	exchanges      []llmxrr.Exchange
}

// runSeam runs `foo -m <model at base> hi` with the cassette seam in
// mode over dir.
func runSeam(t *testing.T, mode, dir, base string) seamRun {
	t.Helper()
	home := t.TempDir()
	journal := filepath.Join(t.TempDir(), "journal.jsonl")
	cmd := exec.Command(fooBin, "-m", "openai://stand-in?api_key=stand-in-key&base_url="+base+"/v1", "hi")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"FOO_SECRETS_BACKEND=env",
		"NO_COLOR=1",
		"XRR_MODE="+mode,
		"XRR_CASSETTE_DIR="+dir,
		"FOO_XRR_JOURNAL="+journal,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		require.Truef(t, errors.As(err, &ee), "run foo: %v", err)
		code = ee.ExitCode()
	}
	exs, err := llmxrr.ReadJournal(journal)
	require.NoError(t, err)
	return seamRun{code: code, stdout: stdout.String(), stderr: stderr.String(), exchanges: exs}
}

func cassetteFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	require.NoError(t, err)
	return m
}

func TestXRRSeam_RecordRefusesFailures(t *testing.T) {
	ensureBinary(t)
	for _, c := range []struct {
		name string
		base func(t *testing.T) (string, *standIn)
		want string
	}{
		{"status 400", func(t *testing.T) (string, *standIn) { s := newStandIn(t, http.StatusBadRequest); return s.URL, s }, "status 400"},
		{"status 500", func(t *testing.T) (string, *standIn) {
			s := newStandIn(t, http.StatusInternalServerError)
			return s.URL, s
		}, "status 500"},
		{"transport error", func(t *testing.T) (string, *standIn) { return deadEndpoint(t), nil }, "transport error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			base, s := c.base(t)
			run := runSeam(t, "record", dir, base)

			require.NotZerof(t, run.code, "recording a failure exited 0\nstderr: %s", run.stderr)
			require.Contains(t, run.stderr, "refusing to record")
			require.Contains(t, run.stderr, c.want)
			require.NotContains(t, run.stderr, "stand-in-key")
			require.Empty(t, cassetteFiles(t, dir), "a failed exchange was written to the cassette")
			require.NotEmpty(t, run.exchanges)
			require.True(t, run.exchanges[0].Refused, "journal does not mark the refusal: %+v", run.exchanges[0])
			if s != nil {
				// The SDK retries a failed request; after the refusal
				// those retries never reach the provider again.
				require.EqualValues(t, 1, s.hits.Load(), "provider hits")
			}
		})
	}
}

// A cassette holding a failure, as recordings made before failures
// were refused do, heals under record mode and replays the success.
func TestXRRSeam_RecordHealsStoredFailure(t *testing.T) {
	ensureBinary(t)
	for _, c := range []struct {
		name  string
		spoil func(resp string) string
	}{
		{"status 400", func(resp string) string {
			return strings.Replace(resp, "status: 200", "status: 400", 1)
		}},
		{"error envelope", func(resp string) string {
			return strings.Replace(resp, "payload:", "error: 'Post \"/v1/chat/completions\": unsupported protocol scheme \"\"'\npayload:", 1)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			ok := newStandIn(t, http.StatusOK)
			first := runSeam(t, "record", dir, ok.URL)
			require.Zerof(t, first.code, "record: %s", first.stderr)
			resps, err := filepath.Glob(filepath.Join(dir, "*.resp.yaml"))
			require.NoError(t, err)
			require.Len(t, resps, 1)
			good, err := os.ReadFile(resps[0])
			require.NoError(t, err)
			spoiled := c.spoil(string(good))
			require.NotEqual(t, string(good), spoiled)
			require.NoError(t, os.WriteFile(resps[0], []byte(spoiled), 0o644))

			// Replay will not pass the stored failure off as a reply.
			bad := runSeam(t, "replay", dir, deadEndpoint(t))
			require.NotZero(t, bad.code)
			require.Contains(t, bad.stderr, "failed exchange")
			require.Contains(t, bad.stderr, "re-record")

			// Recording again sends the request and overwrites it.
			again := newStandIn(t, http.StatusOK)
			healed := runSeam(t, "record", dir, again.URL)
			require.Zerof(t, healed.code, "re-record: %s", healed.stderr)
			require.EqualValues(t, 1, again.hits.Load(), "the stored failure was replayed instead of re-recorded")

			replay := runSeam(t, "replay", dir, deadEndpoint(t))
			require.Zerof(t, replay.code, "replay after heal: %s", replay.stderr)
			require.Contains(t, replay.stdout, "hello")
		})
	}
}
