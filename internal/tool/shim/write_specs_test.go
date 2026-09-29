package shim

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"hop.top/kit/go/core/scope"
)

func TestWriteSpecs_Declarations(t *testing.T) {
	for _, tc := range []struct {
		name, effect, paths string
		escalate            map[string]string // param=true → side effect
		params              []string
	}{
		{"cp", "write", "src:r dst:w", map[string]string{"overwrite": "destructive"}, []string{"dst", "overwrite", "recursive", "src"}},
		{"mv", "write", "src:rw dst:w", map[string]string{"overwrite": "destructive"}, []string{"dst", "overwrite", "src"}},
		{"mkdir", "write", "path:w", nil, []string{"parents", "path"}},
		{"rm", "destructive", "path:w", nil, []string{"dir", "path", "recursive"}},
		{"sed", "destructive", "path:rw", map[string]string{"dry_run": "read"}, []string{"backrefs", "dry_run", "extended", "find", "global", "ignore_case", "occurrence", "path", "replace"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := wTool(t, tc.name)
			s := l.Spec
			if s.SideEffect != tc.effect || s.Network != "none" || s.PathSummary() != tc.paths {
				t.Errorf("side effect %q network %q paths %q", s.SideEffect, s.Network, s.PathSummary())
			}
			for param, want := range tc.escalate {
				vals := values{param: true}
				if got := s.effectiveSideEffect(vals); got != want {
					t.Errorf("%s=true → %q; want %q", param, got, want)
				}
				// A read-only call must not need write on any path.
				if want == "read" {
					for _, p := range s.Params {
						if p.Type == TypePath && s.effectiveOp(p, vals)&scope.Write != 0 {
							t.Errorf("%s=true: %s still needs write", param, p.Name)
						}
					}
				}
			}
			var schema struct {
				Properties map[string]any `json:"properties"`
			}
			// Schema on the BSD/default flavor (no variant).
			if err := json.Unmarshal(s.schema(nil, "/cwd"), &schema); err != nil {
				t.Fatal(err)
			}
			if got := slices.Sorted(maps.Keys(schema.Properties)); !slices.Equal(got, tc.params) {
				t.Errorf("schema params %v; want %v", got, tc.params)
			}
			info := NewExtInfo(l, nil, "0.0.0")
			if info.FooTool.SideEffect != tc.effect || len(info.FooTool.Paths) == 0 {
				t.Errorf("ext-info foo_tool %+v", info.FooTool)
			}
			if tc.name == "sed" {
				ow := info.FooTool.Paths["path"].OpWhen
				if ow == nil || !slices.Equal(ow.Op, []string{"read"}) || ow.When["dry_run"] != true {
					t.Errorf("ext-info sed path op_when %+v; want {dry_run: true} → [read]", ow)
				}
			}
		})
	}
}

// Lint rules for the keys the write specs rely on, checked by editing
// the built-in sources.
func TestLint_WriteSpecKeys(t *testing.T) {
	for _, tc := range []struct{ tool, from, to, want string }{
		{"sed", "  backrefs: backrefs\n", "  backrefs: nope\n", `unknown param "nope"`},
		{"sed", "  backrefs: backrefs\n", "  backrefs: extended\n", `feeds the script`},
		{"sed", "  backrefs: backrefs\n", "  backrefs: find\n", `must be bool`},
		{"sed", "    op_when:\n      when: {dry_run: true}\n", "    op_when:\n      when: {nope: true}\n", `op_when: unknown param "nope"`},
		{"sed", "    op_when:\n      when: {dry_run: true}\n", "    op_when:\n      when: {}\n", `op_when: empty when`},
		{"sed", "      op: [read]\n", "      op: [read, exec]\n", `op_when may only narrow op`},
		{"sed", "      op: [read]\n", "      op: []\n", `op_when: op is required`},
		{"sed", "      op: [read]\n", "      op: [delete]\n", `op_when: op "delete"`},
		{"sed", "      op: [read]\n", "      op: [read, write]\n", `op_when may only narrow op`},
		// Dropping write while the call still has a write side effect.
		{"sed", "    op_when:\n      when: {dry_run: true}\n", "    op_when:\n      when: {global: true}\n", `op_when drops write but the side effect is "destructive"`},
		{"sed", "  - name: find\n    type: string\n", "  - name: find\n    op_when: {when: {dry_run: true}, op: [read]}\n    type: string\n", `path keys on a string param`},
	} {
		t.Run(tc.tool+" "+tc.want, func(t *testing.T) {
			raw := string(wTool(t, tc.tool).Raw)
			src := strings.Replace(raw, tc.from, tc.to, 1)
			if src == raw {
				t.Fatalf("fixture edit %q did not apply", tc.from)
			}
			_, err := Parse([]byte(src))
			var le *LintError
			if !errors.As(err, &le) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v; want lint error containing %q", err, tc.want)
			}
		})
	}
}
