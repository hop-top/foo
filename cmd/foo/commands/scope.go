// `foo scope` — inspect the path policy tool calls are checked against.
//
// The subcommands are kit's (console/cli/scope), mounted over foo's
// policy: gate.LoadScope, the same scope.yaml rules plus secret deny
// list the tool gate enforces. Paths given to check/test are resolved
// the way the gate resolves a tool's path argument (relative to the
// working directory, symlinks and ".." physically) before the decision.

package commands

import (
	"os"

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

func scopeCmd() *cobra.Command {
	cmd := kitscope.Cmd()
	cmd.GroupID = "management"
	cmd.Short = "Inspect which paths tools may read or write"
	cmd.Long = `Inspect the path policy that gates tool calls: which paths a tool
may read, write or execute, from scope.yaml in foo's config directory
(and /etc/xdg/foo/scope.yaml), plus a built-in deny list for secrets
(.env, keys, ~/.ssh, ...). With no scope.yaml every tool call is denied.

Mode strict denies any path no allow rule covers; warn and prompt allow
those paths and only log or ask for paths a deny rule matches.`

	for _, sub := range cmd.Commands() {
		adaptScopeCmd(sub)
	}
	return cmd
}

// scopeLong replaces kit's help text, which documents --tool.
var scopeLong = map[string]string{
	"show": `Print foo's effective scope policy: the mode and every allow and deny
rule, including the built-in secret deny list.`,
	"check": `Check one path against foo's scope policy for an operation (--op
read, write or exec; default read). The path is resolved as a tool's
path argument is: relative to the working directory, "~" to the home
directory, symlinks and ".." physically. Exits 0 when allowed, 1 when
denied.`,
	"test": `Check several paths against foo's scope policy for one operation.
Paths are resolved as for "foo scope check". Exits 1 when any path is
denied.`,
}

// adaptScopeCmd points a kit scope subcommand at foo's policy.
func adaptScopeCmd(sub *cobra.Command) {
	if long, ok := scopeLong[sub.Name()]; ok {
		sub.Long = long
	}
	// kit's --tool re-roots the policy; foo scope always shows foo's.
	_ = sub.Flags().MarkHidden("tool")

	run := sub.RunE
	resolveArgs := sub.Name() == "check" || sub.Name() == "test"
	sub.RunE = func(c *cobra.Command, args []string) error {
		if c.Flags().Changed("tool") {
			return output.UsageError("--tool is not supported: foo scope always reads foo's scope policy")
		}
		sc, err := gate.LoadScope(scopeTool)
		if err != nil {
			return err
		}
		if !sc.Configured() {
			c.PrintErrln("[foo] " + gate.NoScopeMessage(sc.UserFile))
		}
		if resolveArgs {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			for i, a := range args {
				p, err := gate.Canonical(cwd, a)
				if err != nil {
					return output.UsageError(err.Error())
				}
				args[i] = p
			}
		}

		// kit's subcommands read scope.Default() and the global viper;
		// point both at foo's for this run.
		restore := scope.SetDefault(sc.Policy)
		defer restore()
		prevFormat := viper.Get("format")
		viper.Set("format", root.Viper.GetString("format"))
		defer viper.Set("format", prevFormat)

		return run(c, args)
	}
}
