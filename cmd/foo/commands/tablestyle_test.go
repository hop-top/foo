package commands

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"

	"charm.land/lipgloss/v2"
	"hop.top/kit/go/console/output"
)

type styleRow struct {
	Scheme string `json:"scheme" yaml:"scheme" table:"SCHEME,priority=9"`
}

// testStyle is a TableStyle with a border but no colors, so assertions
// key off the box-drawing characters rather than ANSI sequences (which
// lipgloss strips when it decides the terminal has no color profile).
func testStyle() output.TableStyle {
	return output.TableStyle{Border: lipgloss.NormalBorder()}
}

// newStyledForTest builds the formatter under test AND registers it in
// output.Default for the duration of the test, restoring the previous
// entry afterwards.
//
// Registering is not incidental. output.Render resolves the `table` key
// through output.Default, so a formatter that delegates to Render while
// registered there can re-enter itself. A test that renders through an
// unregistered local value bypasses that entirely and would pass even
// with the recursion guard removed — which is exactly how a stack
// overflow reached a hand-run binary once already.
func newStyledForTest(t *testing.T) styledTableFormatter {
	t.Helper()
	prev, ok := output.Default.Lookup(output.Table)
	if !ok {
		t.Fatal("no table formatter registered")
	}
	plain := prev
	if styled, already := plain.(styledTableFormatter); already {
		plain = styled.plain
	}
	f := styledTableFormatter{style: testStyle(), plain: plain}
	output.Default.Override(f)
	t.Cleanup(func() { output.Default.Override(prev) })
	return f
}

// TestStyledTable_NonTTYStaysPlain pins the contract that redirected
// output is unchanged: a buffer is not a terminal, so the tabwriter
// output must come through with no border and no escape sequences.
func TestStyledTable_NonTTYStaysPlain(t *testing.T) {
	f := newStyledForTest(t)
	var buf bytes.Buffer
	if err := f.Render(&buf, []styleRow{{"anthropic"}}, nil, nil); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	if strings.ContainsAny(got, "┌┐└┘│─") {
		t.Errorf("non-TTY output must not be bordered, got:\n%s", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("non-TTY output must not contain ANSI escapes, got %q", got)
	}
	if !strings.Contains(got, "SCHEME") || !strings.Contains(got, "anthropic") {
		t.Errorf("non-TTY output lost its content, got %q", got)
	}
}

// TestStyledTable_TTYIsBordered is the other half: on a real terminal
// the styled renderer runs and draws a border. It uses a pty because
// the TTY test is what selects the path, and a buffer can never
// exercise it.
func TestStyledTable_TTYIsBordered(t *testing.T) {
	ptmx, tty, err := openPTY()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	// Drain the master end concurrently so a write to the slave cannot
	// block on a full pty buffer.
	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- out.String()
	}()

	f := newStyledForTest(t)
	if err := f.Render(tty, []styleRow{{"anthropic"}}, nil, nil); err != nil {
		t.Fatalf("Render: %v", err)
	}
	tty.Close()
	ptmx.Close()
	got := <-done

	if !strings.ContainsAny(got, "┌┐└┘│─") {
		t.Errorf("TTY output must be bordered, got:\n%q", got)
	}
	if !strings.Contains(got, "SCHEME") || !strings.Contains(got, "anthropic") {
		t.Errorf("TTY output lost its content, got %q", got)
	}
}

// TestStyledTable_StructuredFormatsUntouched guards the constraint that
// matters most to scripts: the override is registered under the `table`
// key only, so json/yaml still resolve to their own formatters.
func TestStyledTable_StructuredFormatsUntouched(t *testing.T) {
	// Drive the real install path, not a locally built formatter: the
	// risk being guarded against is installStyledTables touching a key
	// other than `table`, and only calling it can surface that.
	prevTable, _ := output.Default.Lookup(output.Table)
	prevJSON, _ := output.Default.Lookup(output.JSON)
	prevYAML, _ := output.Default.Lookup(output.YAML)
	t.Cleanup(func() {
		output.Default.Override(prevTable)
		output.Default.Override(prevJSON)
		output.Default.Override(prevYAML)
	})
	installStyledTables(New("test"))

	rows := []styleRow{{"anthropic"}}
	// Assert on rendered bytes, not just the formatter's type: a type
	// check alone still passes if json/yaml are swapped for some other
	// formatter that renders nothing.
	want := map[string]string{
		output.JSON: "\"scheme\": \"anthropic\"",
		output.YAML: "scheme: anthropic",
	}
	for _, key := range []string{output.JSON, output.YAML} {
		f, ok := output.Default.Lookup(key)
		if !ok {
			t.Fatalf("no %s formatter registered", key)
		}
		if _, styled := f.(styledTableFormatter); styled {
			t.Errorf("%s formatter was replaced by the styled table formatter", key)
		}
		var buf bytes.Buffer
		if err := f.Render(&buf, rows, nil, nil); err != nil {
			t.Fatalf("%s Render: %v", key, err)
		}
		if got := buf.String(); !strings.Contains(got, want[key]) {
			t.Errorf("%s output = %q, want it to contain %q", key, got, want[key])
		}
		if strings.ContainsAny(buf.String(), "┌┐└┘│─") {
			t.Errorf("%s output must never be bordered, got %q", key, buf.String())
		}
	}
}

// TestInstallStyledTables_Idempotent pins that repeated New() calls
// (every test in this package builds a fresh root) never stack styled
// formatters on top of each other — the plain delegate must stay the
// real built-in, or the non-TTY path would recurse.
func TestInstallStyledTables_Idempotent(t *testing.T) {
	r := New("test")
	installStyledTables(r)
	installStyledTables(r)

	got, ok := output.Default.Lookup(output.Table)
	if !ok {
		t.Fatal("table formatter missing after install")
	}
	styled, isStyled := got.(styledTableFormatter)
	if !isStyled {
		t.Fatal("table formatter is not the styled one after install")
	}
	if _, nested := styled.plain.(styledTableFormatter); nested {
		t.Fatal("styled formatter nested inside itself; non-TTY render would recurse")
	}

	// And the non-TTY path still terminates and prints.
	var buf bytes.Buffer
	if err := styled.Render(&buf, []styleRow{{"openai"}}, nil, nil); err != nil {
		t.Fatalf("Render after double install: %v", err)
	}
	if !strings.Contains(buf.String(), "openai") {
		t.Errorf("lost content after double install, got %q", buf.String())
	}
}

// openPTY allocates a pty pair via /dev/ptmx, which needs no extra
// dependency. It is deliberately tolerant: any failure returns an error
// so the caller skips rather than fails, since a sandbox without
// /dev/ptmx says nothing about the code under test.
func openPTY() (*os.File, *os.File, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	name, err := ptsname(ptmx)
	if err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	if err := unlockpt(ptmx); err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	return ptmx, tty, nil
}
