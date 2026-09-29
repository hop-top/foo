package gate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"hop.top/kit/go/ai/toolspec/policy"
	"hop.top/kit/go/core/scope"
	"hop.top/kit/go/core/xdg"
)

// File names under the tool's config dir.
const (
	ScopeFileName  = "scope.yaml"
	PolicyFileName = "tool-policy.yaml"
)

// Scope is the path policy a gate enforces, with where it came from.
type Scope struct {
	// Policy is the tool's scope.yaml rules plus the secret deny list.
	Policy *scope.Policy
	// Files are the scope.yaml files that exist, system first.
	Files []string
	// UserFile is the per-user scope.yaml path, whether or not it
	// exists. It is OS-specific: on macOS without XDG_CONFIG_HOME it
	// lives under ~/Library/Application Support.
	UserFile string
}

// Configured reports whether any scope.yaml exists. Without one no
// path is granted and every call is denied.
func (s Scope) Configured() bool { return len(s.Files) > 0 }

// LoadScope builds the scope policy for tool: kit's FromConfig (system
// /etc/xdg/<tool>/scope.yaml, then the per-user file) plus a deny rule
// for kit's secret paths on every op, which FromConfig does not add.
// Everything that reports on the policy (the gate, `foo scope`) loads
// it here so they agree.
func LoadScope(tool string) (Scope, error) {
	pol, err := scope.FromConfig(tool)
	if err != nil {
		return Scope{}, err
	}
	pol.DenyOp(scope.Read|scope.Write|scope.Exec, scope.SecretPaths()...)

	dir, err := xdg.RawConfigDir(tool)
	if err != nil {
		return Scope{}, err
	}
	s := Scope{Policy: pol, UserFile: filepath.Join(dir, ScopeFileName)}
	for _, f := range []string{filepath.Join("/etc", "xdg", tool, ScopeFileName), s.UserFile} {
		if _, err := os.Stat(f); err == nil {
			s.Files = append(s.Files, f)
		}
	}
	return s, nil
}

// NoScopeMessage explains why every call is denied when no scope.yaml
// exists and where to create one.
func NoScopeMessage(userFile string) string {
	return fmt.Sprintf("no scope policy: %s does not exist, so no path is granted to tools; "+
		"create it with allow rules (run `foo scope show` to inspect the effective policy)", userFile)
}

// Overlay is foo's layer over kit's default side-effect table: every
// local write prompts. kit auto-allows them; foo treats the scope grant
// as where a tool may write, and the prompt as whether it may now.
func Overlay() policy.Table {
	return policy.Table{Rules: []policy.Rule{{
		SideEffect: policy.SideEffectWrite,
		Network:    policy.NetworkNone,
		Action:     policy.ActionPrompt,
		Reason:     "local write; confirm before a tool changes files",
		Source:     "foo",
	}}}
}

// LoadPolicy returns the side-effect table for tool: kit's default,
// then foo's Overlay, then the user's <config>/tool-policy.yaml when
// it exists. Later layers win on (side_effect, network).
func LoadPolicy(tool string) (policy.Table, error) {
	base, err := policy.LoadOrDefault("")
	if err != nil {
		return policy.Table{}, err
	}
	tbl := policy.Merge(base, Overlay())

	dir, err := xdg.RawConfigDir(tool)
	if err != nil {
		return policy.Table{}, err
	}
	user := filepath.Join(dir, PolicyFileName)
	if _, err := os.Stat(user); errors.Is(err, fs.ErrNotExist) {
		return tbl, nil
	}
	overlay, err := policy.LoadFromFile(user)
	if err != nil {
		return policy.Table{}, err
	}
	return policy.Merge(tbl, overlay), nil
}
