package shim

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

func TestWriteSpecs_Declarations(t *testing.T) {
	for _, tc := range []struct {
		name, effect, paths string
		escalate            map[string]string // param=true → side effect
		params              []string
	}{
		{"cp", "write", "src:r dst:w", map[string]string{"overwrite": "destructive"}, []string{"dst", "overwrite", "recursive", "src"}},
		{"mv", "write", "src:w dst:w", map[string]string{"overwrite": "destructive"}, []string{"dst", "overwrite", "src"}},
		{"mkdir", "write", "path:w", nil, []string{"parents", "path"}},
		{"rm", "destructive", "path:w", nil, []string{"dir", "path", "recursive"}},
		{"sed", "destructive", "path:rw", map[string]string{"dry_run": "read"}, []string{"dry_run", "extended", "find", "global", "ignore_case", "occurrence", "path", "replace"}},
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
		})
	}
}
