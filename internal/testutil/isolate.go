package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// IsolateUserDirs points HOME and every XDG base directory at a fresh
// temp dir for the rest of the process, for a TestMain to call before
// m.Run. A test that sets its own dirs with t.Setenv still wins; every
// other test reads and writes the throwaway tree instead of the
// developer's config, caches and state, and never shares them with a
// concurrent test process. The returned cleanup removes the tree.
//
// The go toolchain's own dirs (GOPATH, GOMODCACHE, GOCACHE, GOENV) are
// pinned to what they resolved to before HOME moved, so a test that
// runs `go build` keeps the developer's module and build caches and
// settings.
func IsolateUserDirs() (cleanup func(), err error) {
	if err := pinGoDirs(); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "foo-test-home-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { _ = os.RemoveAll(root) }
	for name, sub := range map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "config",
		"XDG_CACHE_HOME":  "cache",
		"XDG_DATA_HOME":   "data",
		"XDG_STATE_HOME":  "state",
	} {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			cleanup()
			return nil, err
		}
		if err := os.Setenv(name, dir); err != nil {
			cleanup()
			return nil, err
		}
	}
	return cleanup, nil
}

// pinGoDirs sets the go toolchain's dirs to the values `go env`
// reports now, while HOME is still the developer's. Without a go on
// PATH there is nothing to pin: no test can run `go build`.
func pinGoDirs() error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	out, err := exec.Command(gobin, append([]string{"env", "-json"}, goDirs...)...).Output()
	if err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	var dirs map[string]string
	if err := json.Unmarshal(out, &dirs); err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	for _, name := range goDirs {
		if dirs[name] == "" {
			continue
		}
		if err := os.Setenv(name, dirs[name]); err != nil {
			return err
		}
	}
	return nil
}

// goDirs are the go toolchain settings derived from HOME.
var goDirs = []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"}
