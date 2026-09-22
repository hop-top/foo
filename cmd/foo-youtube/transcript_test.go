package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// json3Fixture is a minimal but real-shaped json3 subtitle payload: two
// timed events with segmented text, matching what YouTube's timedtext
// endpoint returns for an auto-caption track.
const json3Fixture = `{"events":[
  {"tStartMs":0,"segs":[{"utf8":"hello"},{"utf8":" world"}]},
  {"tStartMs":65000,"segs":[{"utf8":"second line"}]}
]}`

// subtitleWritingYTDLP builds a helper binary that behaves like yt-dlp's
// real subtitle mode: it parses -o as an OUTPUT TEMPLATE, expands
// %(id)s, appends ".<sub-lang>.<sub-format>", and writes the subtitle
// payload to THAT FILE — writing nothing to stdout, exactly as yt-dlp
// does.
//
// This is the seam that makes bug 2 catchable. A stubbed ytRunner
// returning canned bytes cannot catch it: the defect was that the real
// yt-dlp writes a file and leaves stdout empty, so any test that hands
// the parser bytes directly passes against broken argv. This fake
// reproduces the file-vs-stdout contract, so `-o -` fails here the same
// way it fails in production.
//
// It also records the argv it received into argvFile, so a test can
// assert exactly which subtitle track was requested.
func subtitleWritingYTDLP(t *testing.T, argvFile, payload string, langs []string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "fake.go")

	quoted := make([]string, len(langs))
	for i, l := range langs {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatalf("marshal lang %q: %v", l, err)
		}
		quoted[i] = string(b)
	}
	langsLiteral := "[]string{" + strings.Join(quoted, ", ") + "}"

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	argvJSON, err := json.Marshal(argvFile)
	if err != nil {
		t.Fatalf("marshal argv path: %v", err)
	}

	prog := `package main

import (
	"os"
	"strings"
)

// writtenLangs are the subtitle tracks this fake "has" for the video.
var writtenLangs = ` + langsLiteral + `

const payload = ` + string(payloadJSON) + `

const argvFile = ` + string(argvJSON) + `

func main() {
	args := os.Args[1:]
	_ = os.WriteFile(argvFile, []byte(strings.Join(args, "\n")), 0o644)

	var tmpl, subLang, subFormat string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			if i+1 < len(args) {
				tmpl = args[i+1]
			}
		case "--sub-lang", "--sub-langs":
			if i+1 < len(args) {
				subLang = args[i+1]
			}
		case "--sub-format":
			if i+1 < len(args) {
				subFormat = args[i+1]
			}
		}
	}
	if tmpl == "" || subFormat == "" {
		os.Exit(0)
	}

	// Expand the output template the way yt-dlp does, then append
	// ".<lang>.<format>" per written track. Note: no special casing of
	// "-" — yt-dlp treats it as a literal filename, which is the whole
	// bug. stdout stays empty either way.
	base := strings.ReplaceAll(tmpl, "%(id)s", "VID123")
	for _, lang := range writtenLangs {
		// yt-dlp only writes a track the request actually selected.
		// --sub-lang is an anchored regex, so "en" selects "en" alone.
		if subLang != "" && subLang != lang {
			continue
		}
		_ = os.WriteFile(base+"."+lang+"."+subFormat, []byte(payload), 0o644)
	}
}
`
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	bin := filepath.Join(dir, "yt-dlp")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, src)
	build.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake yt-dlp: %v\n%s", err, out)
	}
	return dir
}

// withSubtitleYTDLP puts the file-writing fake first on PATH and routes
// ytRunner through the REAL exec path, so argv construction is under
// test rather than stubbed away.
func withSubtitleYTDLP(t *testing.T, argvFile, payload string, langs []string) {
	t.Helper()
	dir := subtitleWritingYTDLP(t, argvFile, payload, langs)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prev := ytRunner
	ytRunner = execYTDLP
	t.Cleanup(func() { ytRunner = prev })

	ytCache = nil
}

// TestFetchTranscript_ReadsSubtitleFile is the regression test for the
// `-o -` defect: yt-dlp writes subtitles to a FILE and leaves stdout
// empty, so a fetch that expects stdout gets nothing and the run emits a
// cheerfully empty document.
//
// Because the fake honors -o as a template and writes no stdout, this
// fails against the old `-o -` argv — which is precisely what a
// runner-stubbing test could never do.
func TestFetchTranscript_ReadsSubtitleFile(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"en"})

	got, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", false)
	if err != nil {
		t.Fatalf("fetchTranscript: %v", err)
	}
	if want := "hello world second line"; got != want {
		t.Errorf("transcript = %q, want %q", got, want)
	}

	// The argv must carry a real output template, never the `-o -`
	// pseudo-stdout form.
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(string(raw), "\n")
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && args[i+1] == "-" {
			t.Error(`argv used "-o -"; yt-dlp treats that as a literal filename, not stdout`)
		}
	}
	if !strings.Contains(string(raw), "%(id)s") {
		t.Errorf("argv lacks an output template:\n%s", raw)
	}
}

// TestFetchTranscript_LeavesNoFilesInCwd proves the fetch does not
// litter the user's working directory. The old `-o -` argv created a
// file literally named "-.en.json3" wherever the process happened to be
// running.
func TestFetchTranscript_LeavesNoFilesInCwd(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"en"})

	// Run from a scratch cwd so any stray write is visible and does not
	// touch the repo.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if _, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", false); err != nil {
		t.Fatalf("fetchTranscript: %v", err)
	}

	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		t.Errorf("stray file left in cwd: %q", e.Name())
	}
}

// TestFetchTranscript_TempDirRemoved proves the downloaded subtitle file
// is cleaned up rather than accumulating in the system temp dir.
func TestFetchTranscript_TempDirRemoved(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"en"})

	before, err := filepath.Glob(filepath.Join(os.TempDir(), "foo-youtube-subs-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if _, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", false); err != nil {
		t.Fatalf("fetchTranscript: %v", err)
	}
	after, err := filepath.Glob(filepath.Join(os.TempDir(), "foo-youtube-subs-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(after) > len(before) {
		t.Errorf("temp dirs leaked: %d before, %d after", len(before), len(after))
	}
}

// TestFetchTranscript_SelectsExactEnglishTrack pins the track selection.
// The probe video exposes "en" alongside "en-orig", "en-en" and hundreds
// of "<lang>-en" machine translations; a loose pattern picks up a
// translated or duplicated transcript. The fake offers the same trap.
func TestFetchTranscript_SelectsExactEnglishTrack(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"en", "en-orig", "en-en", "fr-en"})

	if _, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", false); err != nil {
		t.Fatalf("fetchTranscript: %v", err)
	}

	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(string(raw), "\n")
	var lang string
	for i, a := range args {
		if (a == "--sub-lang" || a == "--sub-langs") && i+1 < len(args) {
			lang = args[i+1]
		}
	}
	if lang != "en" {
		t.Errorf("--sub-lang = %q, want exactly \"en\" (a pattern pulls en-orig and the <lang>-en translations)", lang)
	}
}

// TestFetchTranscript_NoCaptionsIsError is the silent-success guard. A
// video with no English track makes yt-dlp exit 0 having written
// nothing; returning "" from here renders an empty document with a
// cheerful [done] line, which is what hid both defects. It must be a
// real error instead.
func TestFetchTranscript_NoCaptionsIsError(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"fr"}) // no English track

	got, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", false)
	if err == nil {
		t.Fatalf("fetchTranscript returned nil error and %q; a captionless video must fail, not emit an empty document", got)
	}
	if !errors.Is(err, errNoTranscript) {
		t.Errorf("error = %v, want errNoTranscript", err)
	}
}

// TestFetchTranscript_EmptyEventsIsError covers the other empty shape: a
// subtitle file that exists but carries no usable text.
func TestFetchTranscript_EmptyEventsIsError(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, `{"events":[]}`, []string{"en"})

	if _, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", false); !errors.Is(err, errNoTranscript) {
		t.Errorf("error = %v, want errNoTranscript for a subtitle file with no text", err)
	}
}

// TestFetchTranscript_Timestamps proves --timestamps still threads
// through the new file-reading path.
func TestFetchTranscript_Timestamps(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"en"})

	got, err := fetchTranscript(context.Background(), "https://youtu.be/VID123", true)
	if err != nil {
		t.Fatalf("fetchTranscript: %v", err)
	}
	if !strings.Contains(got, "[0:00] hello world") {
		t.Errorf("missing leading timestamp in:\n%s", got)
	}
	if !strings.Contains(got, "[1:05] second line") {
		t.Errorf("missing second timestamp in:\n%s", got)
	}
}
