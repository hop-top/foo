package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// linkEnv is a built foo behind a foo-tool-wc link, plus the isolated
// environment to run it in (throwaway HOME and XDG dirs, minimal PATH,
// no provider keys).
type linkEnv struct {
	link   string
	env    []string
	config string // XDG_CONFIG_HOME
}

func newLinkEnv(t *testing.T) linkEnv {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	foo := filepath.Join(dir, "foo")
	build := exec.Command(gobin, "build", "-buildvcs=false", "-o", foo, "hop.top/foo")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	link := filepath.Join(dir, "foo-tool-wc")
	if err := os.Symlink(foo, link); err != nil {
		t.Fatal(err)
	}
	le := linkEnv{link: link, env: []string{"PATH=" + dir + ":/usr/bin:/bin", "NO_COLOR=1"}}
	for k, sub := range map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data",
		"XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state",
	} {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		le.env = append(le.env, k+"="+d)
		if k == "XDG_CONFIG_HOME" {
			le.config = d
		}
	}
	return le
}

func (le linkEnv) writeConfig(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(le.config, "foo", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type linkResponse struct {
	Error  string `json:"error"`
	Detail struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Path    string `json:"path"`
	} `json:"error_detail"`
	Result *struct {
		OK     bool   `json:"ok"`
		Stdout string `json:"stdout"`
	} `json:"result"`
}

// request pipes one wc request into the link, the way another host
// runs a foo-tool-* plugin. When tty is set the process runs in its
// own session with a pty as controlling terminal (and stderr), so any
// attempt to ask a question there would block until the deadline.
func (le linkEnv) request(t *testing.T, args string, tty bool) (linkResponse, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, le.link)
	cmd.Env = le.env
	cmd.Stdin = strings.NewReader(`{"name":"wc","arguments":` + args + `}`)
	var out bytes.Buffer
	cmd.Stdout = &out
	var errOut func() string
	if tty {
		ptmx, slave, err := openPTY()
		if err != nil {
			t.Skipf("pty unavailable: %v", err)
		}
		defer ptmx.Close()
		done := make(chan string, 1)
		go func() {
			var b bytes.Buffer
			buf := make([]byte, 4096)
			for {
				n, err := ptmx.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			done <- b.String()
		}()
		cmd.Stderr = slave
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 2}
		errOut = func() string {
			_ = slave.Close()
			_ = ptmx.Close()
			return <-done
		}
	} else {
		var eb bytes.Buffer
		cmd.Stderr = &eb
		errOut = eb.String
	}
	err := cmd.Run()
	stderr := errOut()
	if ctx.Err() != nil {
		t.Fatalf("foo-tool-wc hung (asked on its terminal?); stderr=%q", stderr)
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	var resp linkResponse
	if code == 0 {
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatalf("response is not JSON (%v): %q", err, out.String())
		}
	}
	return resp, stderr, code
}

// foo-tool-wc runs the same gate as -T wc, from foo's own scope.yaml,
// but nobody can be asked: a call that needs approval is denied at
// once, without touching the terminal.
func TestShimLink_Gate(t *testing.T) {
	le := newLinkEnv(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"p", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "a.txt"), []byte("one two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inside, outside := wcArgs(root+"/p/a.txt"), wcArgs(root+"/outside/a.txt")

	t.Run("no scope.yaml", func(t *testing.T) {
		resp, stderr, code := le.request(t, inside, false)
		want := filepath.Join(le.config, "foo", "scope.yaml")
		if code != 0 || resp.Result != nil || resp.Detail.Kind != "denied" || !strings.Contains(resp.Detail.Message, want) {
			t.Errorf("exit %d resp %+v stderr %q; want denied naming %s", code, resp, stderr, want)
		}
	})

	le.writeConfig(t, "scope.yaml", "mode: prompt\nallow:\n  - path: \""+root+"/p/**\"\n    ops: [read]\n")
	t.Run("allowed", func(t *testing.T) {
		resp, stderr, code := le.request(t, inside, false)
		if code != 0 || resp.Result == nil || !resp.Result.OK || !strings.Contains(resp.Result.Stdout, root+"/p/a.txt") {
			t.Errorf("exit %d resp %+v stderr %q; want the wc result", code, resp, stderr)
		}
	})
	// mode: prompt asks about a path no rule covers; in link mode that
	// is a denial, even with a terminal attached.
	t.Run("prompt denied without asking", func(t *testing.T) {
		resp, stderr, code := le.request(t, outside, true)
		if code != 0 || resp.Result != nil || resp.Detail.Kind != "denied" || resp.Detail.Path != root+"/outside/a.txt" {
			t.Errorf("exit %d resp %+v; want denied on %s/outside/a.txt", code, resp, root)
		}
		if strings.Contains(stderr, "[y/N]") {
			t.Errorf("a question was asked on the terminal: %q", stderr)
		}
	})

	// A broken policy file is a setup failure, not a refusal: exit 2
	// (usage/config) naming the file, no protocol response.
	bad := le.writeConfig(t, "scope.yaml", "mode: loose\n")
	t.Run("broken scope.yaml", func(t *testing.T) {
		_, stderr, code := le.request(t, inside, false)
		if code != 2 || !strings.Contains(stderr, bad) || !strings.HasPrefix(stderr, "foo-tool-wc: ") {
			t.Errorf("exit %d stderr %q; want 2 naming %s", code, stderr, bad)
		}
		// Discovery does not read the policy: --ext-info still works.
		info := exec.Command(le.link, "--ext-info")
		info.Env = le.env
		if out, err := info.Output(); err != nil || !strings.Contains(string(out), `"name":"wc"`) {
			t.Errorf("--ext-info with a broken scope.yaml: %v %q", err, out)
		}
	})
}
