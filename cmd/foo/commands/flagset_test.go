package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// commandPaths returns the argv path of every command under c, c
// included, in tree order. The root is the empty path.
func commandPaths(c *cobra.Command, prefix []string) [][]string {
	paths := [][]string{append([]string(nil), prefix...)}
	for _, sub := range c.Commands() {
		paths = append(paths, commandPaths(sub, append(prefix, sub.Name()))...)
	}
	return paths
}

// mergeFlagsets forces cobra to fold every inherited persistent flag
// into c's own flagset, the step cobra runs before parsing any argv
// that reaches c. A local flag whose name or shorthand an ancestor's
// persistent flag already owns panics there; it is returned as an
// error instead.
func mergeFlagsets(c *cobra.Command) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	c.InheritedFlags()
	c.LocalFlags()
	return nil
}

// TestCommandTree_FlagsetsMerge builds the flagset of every foo
// command, inherited persistent flags included. kit owns the root's
// globals (-c/--config, -C/--chdir, -V/--verbose, …); a command that
// declares one of those shorthands again builds fine and panics on
// first parse, which no other test reaches until it runs that very
// command. Walking the whole tree catches the next collision wherever
// it lands.
func TestCommandTree_FlagsetsMerge(t *testing.T) {
	newToolTestEnv(t)
	r := New("test")

	for _, path := range commandPaths(r.Cmd, nil) {
		c, _, err := r.Cmd.Find(path)
		if err != nil {
			t.Errorf("find %q: %v", strings.Join(path, " "), err)
			continue
		}
		if err := mergeFlagsets(c); err != nil {
			t.Errorf("%s: flagset does not build: %v", c.CommandPath(), err)
		}
	}
}

// TestCommandTree_HelpRuns renders --help for every foo command
// through kit's Execute, the path main takes, so flags kit attaches at
// dispatch are in play too. The original defect: `foo embed add
// --help` panicked before printing anything.
func TestCommandTree_HelpRuns(t *testing.T) {
	newToolTestEnv(t)

	for _, path := range commandPaths(New("test").Cmd, nil) {
		args := append(append([]string(nil), path...), "--help")
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := New("test")
			var out, errOut bytes.Buffer
			r.Cmd.SetOut(&out)
			r.Cmd.SetErr(&errOut)
			r.Cmd.SetIn(strings.NewReader(""))
			r.SetArgs(args)

			var err error
			func() {
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("panic: %v", p)
					}
				}()
				err = r.Execute(context.Background())
			}()
			if err != nil {
				t.Fatalf("foo %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut.String())
			}
		})
	}
}

// TestEmbed_CollectionFlagLongFormOnly pins the fix for the embed
// collision: --collection keeps its name on every embed leaf that
// takes it, but no shorthand, because -c is kit's --config.
func TestEmbed_CollectionFlagLongFormOnly(t *testing.T) {
	newToolTestEnv(t)
	r := New("test")

	for _, leaf := range []string{"add", "file", "search"} {
		c, _, err := r.Cmd.Find([]string{"embed", leaf})
		if err != nil {
			t.Fatalf("find embed %s: %v", leaf, err)
		}
		if err := mergeFlagsets(c); err != nil {
			t.Fatalf("embed %s: flagset does not build: %v", leaf, err)
		}
		f := c.LocalFlags().Lookup("collection")
		if f == nil {
			t.Errorf("embed %s: --collection missing", leaf)
			continue
		}
		if f.Shorthand != "" {
			t.Errorf("embed %s: --collection shorthand -%s; want none", leaf, f.Shorthand)
		}
		if f.DefValue != "default" {
			t.Errorf("embed %s: --collection default %q; want %q", leaf, f.DefValue, "default")
		}
	}

	// -c still reaches kit's --config on an embed leaf.
	c, _, err := r.Cmd.Find([]string{"embed", "add"})
	if err != nil {
		t.Fatal(err)
	}
	if f := c.Flags().ShorthandLookup("c"); f == nil || f.Name != "config" {
		t.Errorf("embed add -c resolves to %v; want kit's --config", f)
	}
}
