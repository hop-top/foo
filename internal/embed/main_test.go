package embed

import (
	"os"
	"testing"
)

// TestMain routes every non-loopback request through a dead proxy
// before any client runs (net/http reads the proxy variables once), so
// a test whose embedder misses its local recorder fails on the spot
// instead of reaching a real provider. Loopback recorders are never
// proxied.
func TestMain(m *testing.M) {
	for _, v := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		_ = os.Setenv(v, "http://127.0.0.1:9")
	}
	for _, v := range []string{"NO_PROXY", "no_proxy"} {
		_ = os.Unsetenv(v)
	}
	os.Exit(m.Run())
}
