//go:build unix

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// notesPlugin is a third-party foo-tool-notes plugin that declares its
// path parameter under foo_tool. A call appends its request to
// calls.log and answers with the arguments it received.
func notesPlugin(log, footool string) string {
	return `#!/bin/sh
if [ "$1" = "--ext-info" ]; then
  cat <<'JSON'
{"name":"notes","version":"0.1.0","description":"Append a note to a file",
 "parameters":{"type":"object","properties":{"file":{"type":"string"},"text":{"type":"string"}},"required":["file","text"]},
 "foo_tool":` + footool + `}
JSON
  exit 0
fi
req=$(cat)
printf '%s\n' "$req" >> '` + log + `'
printf '{"result":%s}\n' "$req"
`
}

// received is the arguments object the plugin echoed back.
func received(t *testing.T, o outcome) map[string]any {
	t.Helper()
	require.Nilf(t, o.Err, "want the plugin's result, got %s", o.Raw)
	var msg struct {
		Arguments map[string]any `json:"arguments"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Raw), &msg))
	return msg.Arguments
}

// TestToolPlugins_DeclaredPathsGated: a third-party plugin that
// declares foo_tool paths is checked against scope.yaml on the built
// binary and runs with the canonical path; one that is refused never
// runs.
func TestToolPlugins_DeclaredPathsGated(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	notes := e.path("notes")
	mkfile(t, filepath.Join(notes, "todo.md"), "- one\n")
	require.NoError(t, os.MkdirAll(filepath.Join(notes, "sub"), 0o755))
	mkfile(t, e.path("outside", "x.md"), "x\n")
	log := e.path("calls.log")
	footool := `{"spec":1,"side_effect":"write","paths":{"file":{"op":["read","write"],"must_exist":true,"kind":"file"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(e.bin, "foo-tool-notes"), []byte(notesPlugin(log, footool)), 0o755))
	e.scope("mode: strict\nallow:\n  - path: \"" + notes + "/**\"\n    ops: [read, write]\n")

	ran := func() bool { _, err := os.Stat(log); return err == nil }

	t.Run("listed with its paths", func(t *testing.T) {
		code, stdout, stderr := e.run(e.root, nil, "tool", "list", "--format", "json")
		require.Equalf(t, 0, code, "%s", stderr)
		var rows []struct {
			Name       string `json:"name"`
			SideEffect string `json:"side_effect"`
			Paths      string `json:"paths"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
		found := false
		for _, r := range rows {
			if r.Name == "notes" {
				found = true
				require.Equal(t, "write", r.SideEffect)
				require.Equal(t, "file:rw", r.Paths)
			}
		}
		require.Truef(t, found, "notes missing from %s", stdout)
	})
	t.Run("write needs approval, no terminal: declined", func(t *testing.T) {
		o := e.call(t, modeInProcess, notes, "notes", jsonArgs(t, map[string]any{"file": "todo.md", "text": "two"}))
		wantRefused(t, o, "declined", "", "cannot be asked")
		require.False(t, ran(), "declined plugin ran")
	})

	e.policy(allowLocalChanges)
	t.Run("outside the grant: denied", func(t *testing.T) {
		o := e.call(t, modeInProcess, e.root, "notes", jsonArgs(t, map[string]any{"file": e.path("outside", "x.md"), "text": "two"}))
		wantRefused(t, o, "denied", "file", e.path("outside", "x.md"))
		require.False(t, ran(), "denied plugin ran")
	})
	t.Run("relative path runs with the canonical path", func(t *testing.T) {
		o := e.call(t, modeInProcess, notes, "notes", jsonArgs(t, map[string]any{"file": "./sub/../todo.md", "text": "two"}))
		args := received(t, o)
		require.Equal(t, filepath.Join(notes, "todo.md"), args["file"])
		require.Equal(t, "two", args["text"])
		require.True(t, ran())
	})
}
