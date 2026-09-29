// `foo scope` — inspect the path policy tool calls are checked against.
//
// The subcommands are kit's (console/cli/scope), mounted over foo's
// policy: gate.LoadScope, the same scope.yaml rules plus secret deny
// list the tool gate enforces. show runs kit's body. check and test
// keep kit's flags but report the gate's own verdict (gate.Scope.Classify)
// instead of kit's raw decision: kit calls a path no rule covers
// "unknown" and lets it through outside strict mode, where the gate
// denies, asks or warns. Paths given to check/test are resolved the
// way the gate resolves a tool's path argument (relative to the working
// directory, symlinks and ".." physically) before the decision.

package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"hop.top/foo/internal/tool/gate"
	kitscope "hop.top/kit/go/console/cli/scope"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/scope"
)

// scopeTool names foo's scope policy: <config dir>/foo/scope.yaml and
// /etc/xdg/foo/scope.yaml.
const scopeTool = "foo"

// Exit codes of `foo scope check` and `foo scope test`. 0 (allowed),
// 1 (denied, kit's code) and 2 (usage, including a scope.yaml that does
// not load) come from kit's table; the two verdicts kit has no class
// for take foo's own codes above kit's 0-7.
const (
	exitScopePrompt = 8
	exitScopeWarn   = 9
)

// Error codes paired with the exit codes above in the error envelope.
const (
	codeScopePrompt = "SCOPE_PROMPT"
	codeScopeWarn   = "SCOPE_WARN"
)

func scopeCmd() *cobra.Command {
	cmd := kitscope.Cmd()
	cmd.GroupID = "management"
	cmd.Short = "Inspect which paths tools may read or write"
	cmd.Long = `Inspect the path policy that gates tool calls: which paths a tool
may read, write or execute, from scope.yaml in foo's config directory
(and /etc/xdg/foo/scope.yaml), plus a built-in deny list for secrets
(.env, keys, secrets*, ~/.ssh, ...) that also covers everything under a
directory with such a name. With no scope.yaml every tool call is
denied.

A path a deny rule matches, or that no allow rule covers, is handled by
the mode: strict denies the call, prompt asks once per call (showing
each path, operation and reason; with no terminal the call is denied),
and warn logs one warning per call and runs it. Walks such as grep's
skip those entries in strict and prompt modes.

check and test print what a tool call would do with a path (allowed,
denied, prompt or warn) and exit with a code per verdict; see
"foo scope check --help".`

	for _, sub := range cmd.Commands() {
		adaptScopeCmd(sub)
	}
	return cmd
}

// scopeVerdicts documents check/test output and exit codes.
const scopeVerdicts = `DECISION is what a tool call would do with the path under the
policy's mode, and REASON the rule (or missing rule) behind it:

  allowed  an allow rule covers the path; the call runs         exit 0
  denied   strict mode: a deny rule matches or no allow rule
           covers the path; also every path without scope.yaml  exit 1
  prompt   prompt mode: such a path asks for approval first
           (denied when there is no terminal)                   exit 8
  warn     warn mode: such a path runs with a warning logged    exit 9

A scope.yaml that does not load, or a bad flag or path, exits 2.`

// scopeLong replaces kit's help text, which documents --tool and kit's
// own decisions.
var scopeLong = map[string]string{
	"show": `Print foo's effective scope policy: the mode and every allow and deny
rule, including the built-in secret deny list.`,
	"check": `Check one path against foo's scope policy for an operation (--op
read, write or exec; default read). The path is resolved as a tool's
path argument is: relative to the working directory, "~" to the home
directory, symlinks and ".." physically.

` + scopeVerdicts,
	"test": `Check several paths against foo's scope policy for one operation, one
row per path. Paths are resolved as for "foo scope check". The exit
code is the most restrictive verdict: 1 if any path is denied, else 8
if any would prompt, else 9 if any would warn, else 0.

` + scopeVerdicts,
}

var scopeShort = map[string]string{
	"check": "Show what a tool call would do with one path",
	"test":  "Show what tool calls would do with several paths",
}

// adaptScopeCmd points a kit scope subcommand at foo's policy.
func adaptScopeCmd(sub *cobra.Command) {
	if long, ok := scopeLong[sub.Name()]; ok {
		sub.Long = long
	}
	if short, ok := scopeShort[sub.Name()]; ok {
		sub.Short = short
	}
	// kit's --tool re-roots the policy; foo scope always shows foo's.
	_ = sub.Flags().MarkHidden("tool")

	kitRun := sub.RunE
	verdicts := sub.Name() == "check" || sub.Name() == "test"
	sub.RunE = func(c *cobra.Command, args []string) error {
		if c.Flags().Changed("tool") {
			return output.UsageError("--tool is not supported: foo scope always reads foo's scope policy")
		}
		sc, err := gate.LoadScope(scopeTool)
		if err != nil {
			// A broken scope.yaml is a config error, not a denial:
			// callers branching on the exit code must tell them apart.
			return output.UsageError(err.Error()).Retaining(err)
		}
		if !sc.Configured() {
			c.PrintErrln("[foo] " + gate.NoScopeMessage(sc.UserFile))
		}
		if verdicts {
			return runScopeVerdicts(c, sc, args, sub.Name() == "check")
		}

		// kit's show reads scope.Default() and the global viper; point
		// both at foo's for this run.
		restore := scope.SetDefault(sc.Policy)
		defer restore()
		prevFormat := viper.Get("format")
		viper.Set("format", root.Viper.GetString("format"))
		defer viper.Set("format", prevFormat)

		return kitRun(c, args)
	}
}

// scopeRow is one (path, op) verdict of check or test.
type scopeRow struct {
	Path     string `table:"PATH"     json:"path"             yaml:"path"`
	Op       string `table:"OP"       json:"op"               yaml:"op"`
	Decision string `table:"DECISION" json:"decision"         yaml:"decision"`
	Reason   string `table:"REASON"   json:"reason,omitempty" yaml:"reason,omitempty"`
}

// runScopeVerdicts classifies each path as the gate does, prints one
// row per path (a single object for check) and returns the exit error
// of the most restrictive verdict.
func runScopeVerdicts(c *cobra.Command, sc gate.Scope, args []string, single bool) error {
	opFlag, _ := c.Flags().GetString("op")
	op, err := parseScopeOp(opFlag)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	rows := make([]scopeRow, 0, len(args))
	worst, count := gate.VerdictAllow, 0
	for _, a := range args {
		p, err := gate.Canonical(cwd, a)
		if err != nil {
			return output.UsageError(err.Error())
		}
		v, reason := sc.Classify(p, op)
		rows = append(rows, scopeRow{Path: p, Op: opLabel(op), Decision: v.String(), Reason: reason})
		switch {
		case v > worst:
			worst, count = v, 1
		case v == worst:
			count++
		}
	}

	var data any = rows
	if single {
		data = rows[0]
	}
	if err := output.Dispatch(c, root.Viper, data); err != nil {
		return err
	}
	return verdictExit(worst, count, len(rows))
}

// verdictExit is the exit error for the most restrictive verdict v,
// held by n of total paths; nil when every path is allowed.
func verdictExit(v gate.Verdict, n, total int) error {
	subject := "path"
	if total > 1 {
		subject = fmt.Sprintf("%d of %d paths", n, total)
	}
	var e *output.Error
	switch v {
	case gate.VerdictAllow:
		return nil
	case gate.VerdictDeny:
		// kit's denied code: GENERIC, exit 1.
		e = output.GenericError(subject + " denied: a tool call would be refused")
	case gate.VerdictPrompt:
		e = &output.Error{Code: codeScopePrompt, ExitCode: exitScopePrompt,
			Message: subject + " would prompt: a tool call would ask for approval first, and is denied without a terminal"}
	case gate.VerdictWarn:
		e = &output.Error{Code: codeScopeWarn, ExitCode: exitScopeWarn,
			Message: subject + " would warn: a tool call would run and log a warning"}
	default:
		return fmt.Errorf("scope: unknown verdict %s", v)
	}
	return e.WithTransience(output.TransiencePermanent)
}

// parseScopeOp reads --op as kit's scope commands do.
func parseScopeOp(s string) (scope.Op, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "read", "r":
		return scope.Read, nil
	case "write", "w":
		return scope.Write, nil
	case "exec", "x":
		return scope.Exec, nil
	default:
		return 0, output.UsageError(fmt.Sprintf("unknown op %q (want read|write|exec)", s))
	}
}

func opLabel(op scope.Op) string {
	switch op {
	case scope.Write:
		return "write"
	case scope.Exec:
		return "exec"
	default:
		return "read"
	}
}
