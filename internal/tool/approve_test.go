package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// answers returns an opener that serves the given terminal input and
// counts how often it was asked to open.
func answers(input string, opens *int) func() (io.Reader, error) {
	return func() (io.Reader, error) {
		*opens++
		return strings.NewReader(input), nil
	}
}

func TestPrompterConfirm_Answers(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"  YES  \n", true},
		{"y", true}, // answer at EOF without a newline still counts
		{"n\n", false},
		{"\n", false},
		{"sure\n", false},
		{"", false}, // EOF with no answer is a no
	}
	for _, tc := range cases {
		var opens int
		var out bytes.Buffer
		p := NewPrompter(answers(tc.input, &opens), &out)
		got, err := p.Confirm("go ahead?")
		if err != nil {
			t.Fatalf("input %q: unexpected error %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("input %q: got %v, want %v", tc.input, got, tc.want)
		}
		if !strings.HasPrefix(out.String(), "go ahead? [y/N] ") {
			t.Errorf("input %q: question not written: %q", tc.input, out.String())
		}
	}
}

// A single terminal reader must serve every question. A fresh scanner
// per question buffers past the first line and drops the answers that
// were already typed for the questions after it.
func TestPrompterConfirm_SequentialAnswersShareOneReader(t *testing.T) {
	var opens int
	p := NewPrompter(answers("y\nn\ny\n", &opens), io.Discard)
	var got []bool
	for range 3 {
		ok, err := p.Confirm("q?")
		if err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		got = append(got, ok)
	}
	if want := []bool{true, false, true}; !equalBools(got, want) {
		t.Fatalf("answers = %v, want %v", got, want)
	}
	if opens != 1 {
		t.Fatalf("terminal opened %d times, want 1", opens)
	}
}

func TestPrompter_OpensLazily(t *testing.T) {
	var opens int
	_ = NewPrompter(answers("y\n", &opens), io.Discard)
	if opens != 0 {
		t.Fatalf("terminal opened at construction; want lazy open on first question")
	}
}

func TestPrompterConfirm_NoTerminal(t *testing.T) {
	opens := 0
	openErr := errors.New("open /dev/tty: device not configured")
	var out bytes.Buffer
	p := NewPrompter(func() (io.Reader, error) {
		opens++
		return nil, openErr
	}, &out)

	for range 2 {
		ok, err := p.Confirm("go ahead?")
		if ok {
			t.Fatal("Confirm approved without a terminal")
		}
		if !errors.Is(err, ErrNoTerminal) {
			t.Fatalf("err = %v, want ErrNoTerminal", err)
		}
		if !strings.Contains(err.Error(), openErr.Error()) {
			t.Fatalf("err %q hides the open failure %q", err, openErr)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("question written with no terminal to answer it: %q", out.String())
	}
	if opens != 1 {
		t.Fatalf("open attempted %d times, want 1", opens)
	}
}

func TestPrompterApprove_NoTerminalDeniesWithReason(t *testing.T) {
	var out bytes.Buffer
	p := NewPrompter(func() (io.Reader, error) {
		return nil, errors.New("open /dev/tty: device not configured")
	}, &out)

	if p.Approve("foo_time", json.RawMessage(`{}`)) {
		t.Fatal("Approve returned true without a terminal")
	}
	msg := out.String()
	for _, want := range []string{"foo_time", "denied", "no terminal", "device not configured"} {
		if !strings.Contains(msg, want) {
			t.Errorf("denial message %q missing %q", msg, want)
		}
	}
}

func TestPrompterApprove_AsksWithToolAndArgs(t *testing.T) {
	var opens int
	var out bytes.Buffer
	p := NewPrompter(answers("y\n", &opens), &out)
	if !p.Approve("foo_time", json.RawMessage(`{"tz":"UTC"}`)) {
		t.Fatal("Approve returned false on y")
	}
	if want := `[tool] execute foo_time with {"tz":"UTC"}? [y/N] `; out.String() != want {
		t.Fatalf("prompt = %q, want %q", out.String(), want)
	}
}

func TestOpenTerminal_RejectsNonTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-tty")
	if err := os.WriteFile(path, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := openTerminal(path)
	if err == nil {
		t.Fatalf("openTerminal(%s) = %v, want error for a regular file", path, r)
	}
	if !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("err = %v, want a not-a-terminal error", err)
	}
}

func TestOpenTerminal_MissingDevice(t *testing.T) {
	if _, err := openTerminal(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("openTerminal on a missing path returned no error")
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
