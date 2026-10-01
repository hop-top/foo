// Package testutil holds helpers shared by foo's tests. Nothing in the
// foo binary imports it.
package testutil

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// warmEnv makes a script written by WriteScript exit before its body.
const warmEnv = "FOO_TEST_WARMUP"

// warmTimeout bounds the warm-up run; it only has to outlast the
// first-exec check, never a production timeout.
const warmTimeout = 2 * time.Minute

// WriteScript writes script, which must start with a "#!" line, to path
// as an executable and runs it once before returning.
//
// The first exec of a freshly written file is slow on macOS, and the
// cost grows with load: a fraction of a second on an idle machine,
// past 5s with several test processes building and starting binaries
// at once, while the second exec of the same file takes milliseconds.
// Code under test runs these scripts under its own timeouts (2s for a
// flavor probe, kit's 5s for --ext-info); run cold, the script misses
// it and the test sees a binary that never answered. The warm-up pays
// the first-exec cost here, under no timeout that matters.
//
// The warm-up runs with FOO_TEST_WARMUP set, and WriteScript inserts a
// line after the "#!" line that exits on it, so the body never runs: a
// log, a counter or a "never ran" check is not disturbed.
func WriteScript(t testing.TB, path, script string) string {
	t.Helper()
	shebang, body, ok := strings.Cut(script, "\n")
	if !ok || !strings.HasPrefix(shebang, "#!") {
		t.Fatalf("WriteScript %s: script must start with a #! line", path)
	}
	guard := `[ -n "$` + warmEnv + `" ] && exit 0`
	if err := os.WriteFile(path, []byte(shebang+"\n"+guard+"\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(), warmEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("warm up %s: %v %q", path, err, out)
	}
	return path
}
