package tool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// ErrNoTerminal reports that no terminal is available to read a
// confirmation answer from, so the question cannot be asked.
var ErrNoTerminal = errors.New("no terminal to read the answer from")

// ttyPath is the controlling terminal on Unix. Other platforms fail to
// open it and fall back to ErrNoTerminal.
const ttyPath = "/dev/tty"

// OpenTTY opens the controlling terminal for reading answers. It fails
// when the process has none (CI, cron, a detached session) or when the
// device is not a terminal.
//
// Answers never come from stdin: stdin carries prompt data
// (`echo ... | foo`), and by the time a tool runs it has been read to
// EOF.
func OpenTTY() (io.Reader, error) {
	return openTerminal(ttyPath)
}

func openTerminal(path string) (io.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !term.IsTerminal(int(f.Fd())) {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a terminal", path)
	}
	return f, nil
}

// Prompter asks yes/no questions: the question goes to w, the answer is
// read from the source returned by open. The source is opened on the
// first question only, and one buffered reader serves every question
// so answers typed ahead are not lost.
type Prompter struct {
	open func() (io.Reader, error)
	w    io.Writer

	mu      sync.Mutex
	opened  bool
	in      *bufio.Reader
	openErr error
}

// NewPrompter returns a Prompter that writes questions to w and reads
// answers from the reader open returns. Pass OpenTTY in production.
func NewPrompter(open func() (io.Reader, error), w io.Writer) *Prompter {
	return &Prompter{open: open, w: w}
}

// Confirm asks question and reports whether the answer was yes (y or
// yes, any case). An empty answer or end of input is a no. When no
// answer source can be opened it asks nothing and returns an error
// wrapping ErrNoTerminal.
func (p *Prompter) Confirm(question string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.opened {
		p.opened = true
		r, err := p.open()
		if err != nil {
			p.openErr = fmt.Errorf("%w (%v)", ErrNoTerminal, err)
		} else {
			p.in = bufio.NewReader(r)
		}
	}
	if p.openErr != nil {
		return false, p.openErr
	}

	_, _ = fmt.Fprintf(p.w, "%s [y/N] ", question)
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read answer: %w", err)
	}
	if err != nil && line == "" {
		// End of input with no answer: finish the prompt line.
		_, _ = fmt.Fprintln(p.w)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// Approve is an ApproveFunc that asks before a tool call runs. When the
// question cannot be asked the call is denied and the reason is
// written, so a denial is never silent.
func (p *Prompter) Approve(name string, args json.RawMessage) bool {
	ok, err := p.Confirm(fmt.Sprintf("[tool] execute %s with %s?", name, string(args)))
	if err != nil {
		_, _ = fmt.Fprintf(p.w, "[tool] %s denied: cannot ask for approval: %v\n", name, err)
		return false
	}
	return ok
}
