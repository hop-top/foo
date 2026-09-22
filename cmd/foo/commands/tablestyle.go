package commands

import (
	"io"
	"os"

	"golang.org/x/term"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
)

// styledTableFormatter renders `table` output through kit's themed
// lipgloss renderer instead of the built-in tabwriter.
//
// Why foo's tables were unstyled: the styled renderer is reachable only
// from output.Render, and Render is not what a command calls. Commands
// call output.Dispatch, which resolves --format / --output / --cols /
// --template and then invokes Formatter.Render on the registered
// formatter directly. Formatter.Render takes no RenderOption, so
// WithTableStyle cannot reach it, and the process-wide default style
// cli.New installs (output.SetDefaultTableStyle, from Config.Accent via
// Root.Theme) is read by Render alone. Dispatch therefore always landed
// on the plain tabwriter — which is why kit's own themed surfaces, which
// call Render, looked different from foo's.
//
// Overriding the `table` key is the registry's documented escape hatch
// ("Adopters intentionally replacing a built-in must call Override") and
// keeps every Dispatch feature intact: format resolution, --output file
// writing with its extension/format mismatch check, --cols validation and
// ordering, and --template all still run. Only the final table write is
// re-routed.
//
// Structured formats are untouched by construction: this formatter is
// registered under the `table` key alone, so --format json|yaml|csv
// resolve to their own formatters and never reach this code.
type styledTableFormatter struct {
	style output.TableStyle
	// plain is the built-in table formatter captured before the
	// Override. Delegating to it is what keeps non-TTY output on the
	// tabwriter, and — because output.Render resolves the `table` key
	// through output.Default, which now holds *this* formatter —
	// calling it rather than Render is also what stops the non-styled
	// path recursing into itself.
	plain output.Formatter
}

func (styledTableFormatter) Key() string                  { return output.Table }
func (styledTableFormatter) Extensions() []string         { return nil }
func (styledTableFormatter) Options() []output.OptionSpec { return nil }

// Render writes the styled table on a TTY and the plain one everywhere
// else, so redirected output stays ANSI-free and diff-friendly.
//
// The TTY test is made here rather than left to Render because Render
// only skips its own Default.Lookup("table") result when it takes the
// styled branch; entering it for a non-TTY writer would re-enter this
// formatter and recurse until the stack overflows. Gating on the same
// condition Render gates on (an *os.File attached to a terminal) means
// Render is called only when it is certain to render the styled table
// and never to call back into the registry.
func (f styledTableFormatter) Render(w io.Writer, data any, opts output.Options, cols []string) error {
	if !writerIsTerminal(w) {
		return f.plain.Render(w, data, opts, cols)
	}
	ropts := []output.RenderOption{output.WithTableStyle(f.style)}
	if len(cols) > 0 {
		ropts = append(ropts, output.WithCols(cols))
	}
	return output.Render(w, output.Table, data, ropts...)
}

// writerIsTerminal mirrors the check output.Render applies before
// selecting the styled path: only an *os.File on a terminal qualifies.
// Buffers, pipes and files report false.
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// installStyledTables points the `table` formatter at the active CLI
// theme. Called from New once the root — and therefore the theme —
// exists. Re-entrant across repeated New() calls in tests: the built-in
// is re-read from the registry each time, and a second install captures
// the formatter installed by the first as its plain delegate only if it
// is not itself styled.
func installStyledTables(r *kitcli.Root) {
	if r == nil {
		return
	}
	plain, ok := output.Default.Lookup(output.Table)
	if !ok {
		return
	}
	if styled, already := plain.(styledTableFormatter); already {
		plain = styled.plain
	}
	output.Default.Override(styledTableFormatter{style: r.TableStyle(), plain: plain})
}
