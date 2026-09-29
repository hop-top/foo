// Package gate decides whether a shim tool call may run: it resolves
// every path argument to its canonical physical form, checks it against
// the user's scope policy for the declared operation, applies the
// side-effect policy table, and asks for approval when either says so.
//
// The shim engine calls Authorize after validating arguments and before
// building argv. It must pass only the canonical paths from the Grant to
// the command, never the raw values the model sent, so the path that
// was checked is the path that runs.
package gate

import (
	"context"
	"fmt"

	"hop.top/kit/go/core/scope"
)

// Target says what a path argument names when its final component is a
// symlink.
type Target int

const (
	// Follow names the content: the final symlink is resolved (cat, ls, grep).
	Follow Target = iota
	// Dirent names the directory entry itself: the link, not its target
	// (rm, mv, cp destination, mkdir).
	Dirent
)

// Recursion says how a recursive path argument is checked.
type Recursion int

const (
	// NoRecursion checks the path alone.
	NoRecursion Recursion = iota
	// FilterBefore walks the tree without following links and grants only
	// the allowed regular files (grep).
	FilterBefore
	// FilterAfter grants the root and checks each output entry afterwards
	// through Grant.Allow (find).
	FilterAfter
	// AllOrNothing walks the tree and denies the whole call if any entry
	// is denied (rm -r, cp -R, mv of a directory).
	AllOrNothing
)

// PathArg is one path-typed parameter of a call, as declared by the spec.
type PathArg struct {
	// Param is the parameter name, reported back in errors.
	Param string
	// Values are the raw strings the model sent, in order.
	Values []string
	// Op is the filesystem operation the command performs on the path.
	Op scope.Op
	// Target selects follow or dirent resolution of the final component.
	Target Target
	// IntoDir maps an existing directory destination to dst/<base(src)>.
	IntoDir bool
	// MustExist fails the call when the path does not exist.
	MustExist bool
	// Parents checks every missing ancestor for write (mkdir -p).
	Parents bool
	// Recursion selects how a tree under the path is checked.
	Recursion Recursion
}

// Request is one tool call to authorize.
type Request struct {
	// Tool is the tool's name as the model sees it.
	Tool string
	// SideEffect is the effective class after side_effect_if:
	// "read", "write" or "destructive".
	SideEffect string
	// Paths are the call's path arguments.
	Paths []PathArg
	// Argv renders the command that will run from the canonical paths.
	// Approval prompts show it instead of the model's raw arguments.
	Argv func(canonical map[string][]string) []string
}

// Grant is a successful authorization.
type Grant struct {
	// Canonical maps each Param to its canonical absolute paths, in the
	// order of PathArg.Values (after IntoDir mapping).
	Canonical map[string][]string
	// Files maps a FilterBefore Param to the allowed regular files under
	// its root, in walk order.
	Files map[string][]string
	// Allow checks one output entry of a FilterAfter call. Nil when the
	// call has no FilterAfter argument.
	Allow func(abs string) bool
	// Filtered counts entries withheld by FilterBefore walking.
	Filtered int
}

// Authorizer authorizes tool calls.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (Grant, error)
}

// Kind classifies why a call was refused. Values match the error kinds of
// the tool-message envelope sent to the model.
type Kind string

const (
	KindDenied      Kind = "denied"
	KindPolicy      Kind = "policy"
	KindDeclined    Kind = "declined"
	KindInvalidArgs Kind = "invalid_args"
	KindNotFound    Kind = "not_found"
)

// Error is a refused call. The command never ran.
type Error struct {
	Kind    Kind
	Param   string
	Path    string
	Op      scope.Op
	Message string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("%s: %s %s: %s", e.Kind, e.Param, e.Path, e.Message)
}
