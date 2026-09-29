package gate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
// for SecretPatterns on every op, which FromConfig does not add.
// Everything that reports on the policy (the gate, `foo scope`) loads
// it here so they agree.
func LoadScope(tool string) (Scope, error) {
	pol, err := scope.FromConfig(tool)
	if err != nil {
		return Scope{}, err
	}
	pol.DenyOp(scope.Read|scope.Write|scope.Exec, SecretPatterns()...)

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

// SecretPatterns is the secret deny list foo enforces: kit's
// SecretPaths, then the anywhere forms of kit's home credential dirs
// (anywhereDirs) and of anywhereFiles, each followed by its descendant
// form "<pattern>/**".
//
// kit's patterns name an entry (**/secrets*, **/credentials*, **/.env,
// **/*.pem, ...) and match that entry alone, so a directory with such a
// name was denied while the files under it were not. The descendant
// form covers them. It is added to every pattern that does not already
// end in "/**", file-like names included: where the name is a file it
// matches nothing more, and where a directory takes the name its
// contents are as secret as the file would be.
//
// kit anchors credential stores to home (~/.ssh/**, ~/.aws/**, ~/.netrc,
// ...), so a copy inside a granted tree (a project's .ssh/, a checked-out
// dotfiles repo, a home backup) was readable. The anywhere forms deny
// the same dir or file at any depth. Duplicates are dropped, so a kit
// that returns descendant or anywhere forms itself yields the same set.
func SecretPatterns() []scope.Pattern {
	return secretPatterns(scope.SecretPaths())
}

// anywhereFiles are home credential files foo denies at any depth: each
// holds a password or token wherever it sits. .npmrc is left out: a
// project .npmrc is registry and install config tools need to read, and
// a token in it is usually an ${ENV} reference.
var anywhereFiles = []scope.Pattern{"**/.netrc", "**/.pgpass", "**/.pypirc", "**/.my.cnf"}

func secretPatterns(kit []scope.Pattern) []scope.Pattern {
	all := slices.Concat(kit, anywhereDirs(kit), anywhereFiles)
	return withDescendants(all)
}

// anywhereDirs returns "**/<rel>" for each kit pattern "~/<rel>/**"
// naming a whole directory under home, in order, without duplicates.
// rel is kept whole (.config/gcloud, not gcloud) so the form names the
// same store rather than any dir sharing its last name. Patterns with
// globs in rel, and a rel that is one segment without a leading dot
// (~/Documents/**), are skipped: they name no specific store.
func anywhereDirs(pats []scope.Pattern) []scope.Pattern {
	var out []scope.Pattern
	for _, p := range pats {
		s := string(p)
		if !strings.HasPrefix(s, "~/") || !strings.HasSuffix(s, "/**") {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(s, "~/"), "/**")
		if rel == "" || strings.ContainsAny(rel, `*?[]{}\`) {
			continue
		}
		if !strings.Contains(rel, "/") && !strings.HasPrefix(rel, ".") {
			continue
		}
		if a := scope.Pattern("**/" + rel); !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// withDescendants returns pats with "<p>/**" after each p that does not
// already end in "/**", in order, without duplicates.
func withDescendants(pats []scope.Pattern) []scope.Pattern {
	out := make([]scope.Pattern, 0, 2*len(pats))
	seen := make(map[scope.Pattern]bool, 2*len(pats))
	add := func(p scope.Pattern) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range pats {
		add(p)
		if !strings.HasSuffix(string(p), "/**") {
			add(p + "/**")
		}
	}
	return out
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
