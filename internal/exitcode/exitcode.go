// Package exitcode lists the process exit codes foo assigns itself.
// kit owns 0–7 (success, generic, usage, not found, conflict,
// unauthorized, …); foo's own codes start at 8 so scripts can tell
// these outcomes apart from kit's classes.
package exitcode

const (
	// ScopePrompt: `foo scope check|test` — a tool call on the path
	// would ask for approval first.
	ScopePrompt = 8
	// ScopeWarn: `foo scope check|test` — a tool call on the path would
	// run and log a warning.
	ScopeWarn = 9
	// Offline: --offline refused a model call to a non-loopback
	// endpoint.
	Offline = 10
)
