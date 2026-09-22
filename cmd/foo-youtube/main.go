// foo-youtube is a standalone binary that extracts a YouTube video's
// transcript and metadata as markdown. It is an external plugin for
// foo: the host discovers it on $PATH and interrogates it via
// --ext-info (kit ai/ext/discover). It imports zero foo internal
// packages.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/xdg"
	kitbus "hop.top/kit/go/runtime/bus"
	"hop.top/kit/go/storage/kv"
	// kit v0.5 splits kv backends into separately-imported packages;
	// the blank import runs the init() that registers "sqlite".
	_ "hop.top/kit/go/storage/kv/sqlite"
)

var version = "dev"

// ytCache caches raw yt-dlp invocation output keyed by argv, so repeated
// extractions of the same video skip the subprocess + network. nil when
// caching is disabled (--no-cache or a store-open failure); runYTDLP
// tolerates the nil cache and just execs. Initialized once in run().
var ytCache kv.TTLStore

// ytCacheTTL is the freshness window for cached yt-dlp output. Resolved
// in openYTCache from FOO_YOUTUBE_CACHE_TTL, then the host-level
// FOO_CACHE_TTL this extension inherits, then youtubeCacheTTLDefault.
var ytCacheTTL = youtubeCacheTTLDefault

// youtubeCacheDB is this extension's sqlite filename under a cache
// directory, so extensions inheriting foo's FOO_CACHE keep distinct
// stores.
const youtubeCacheDB = "ytdlp-cache.db"

// youtubeCacheTTLDefault is the freshness window when neither
// FOO_YOUTUBE_CACHE_TTL nor FOO_CACHE_TTL is set.
const youtubeCacheTTLDefault = 24 * time.Hour

// eventBus carries capture events to external subscribers (aps, ctxt,
// tlc) via the network adapter. A bare bus.New() publishes in-process to
// nobody; wireBusNetwork attaches the adapter when peers are configured.
// nil until run() initializes it; publishEvent tolerates the nil bus.
var (
	eventBus kitbus.Bus
	busNet   *kitbus.NetworkAdapter
)

// Exit codes follow the kit cross-tool convention (§8.1): 1 generic,
// 2 usage (bad/missing args), 5 a missing external dependency. Fetch
// failures are runtime errors against an otherwise-valid request, so
// they map to the generic 1. Cobra itself exits 2 on flag/arg parse
// failures, which lines up with exitUsage.
const (
	exitFetch      = 1
	exitUsage      = 2
	exitMissingDep = 5
)

// codeMissingDep is the structured error code emitted when the yt-dlp
// dependency is absent. Its exit code (5) matches the §8.1 slot kit
// reserves for environment/auth failures; the label is plugin-specific.
const codeMissingDep = "MISSING_DEPENDENCY"

// exitError carries a kit structured-error envelope out of RunE. kit's
// RunE middleware reads AsCLIError to render + return the envelope; main
// then reads its ExitCode to pick the process exit status (§8.1).
type exitError struct {
	cli *output.Error
}

func (e *exitError) Error() string { return e.cli.Error() }

func (e *exitError) AsCLIError() *output.Error { return e.cli }

func usageErrorf(format string, a ...any) *exitError {
	return &exitError{cli: output.UsageError(fmt.Sprintf(format, a...))}
}

func missingDepError(err error) *exitError {
	return &exitError{cli: &output.Error{
		Code:     codeMissingDep,
		Message:  err.Error(),
		ExitCode: exitMissingDep,
	}}
}

func fetchErrorf(format string, a ...any) *exitError {
	return &exitError{cli: &output.Error{
		Code:     output.CodeGeneric,
		Message:  fmt.Sprintf(format, a...),
		ExitCode: exitFetch,
	}}
}

type extInfo struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

type videoMetadata struct {
	Title       string `json:"title"`
	Channel     string `json:"channel"`
	UploadDate  string `json:"upload_date"`
	Description string `json:"description"`
	Duration    int    `json:"duration"`
	ViewCount   int    `json:"view_count"`
	LikeCount   int    `json:"like_count"`
}

type comment struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

// extInfoArg is the host's discovery probe. kit's ai/ext/discover runs
// the binary as `foo-youtube --ext-info` and json-decodes stdout, so
// this is a hard wire contract: emit ONLY the JSON object, exit 0.
const extInfoArg = "--ext-info"

func main() {
	// Honor the --ext-info wire contract before cobra parses anything.
	// The host invokes the binary with exactly this single flag and
	// parses stdout as JSON, so we must keep stdout clean of any cobra
	// help/usage chrome and guarantee exit 0.
	for _, a := range os.Args[1:] {
		if a == extInfoArg {
			if err := printExtInfo(os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "error encoding ext-info: %v\n", err)
				os.Exit(exitFetch)
			}
			return
		}
	}

	root := newRoot()
	if err := root.Execute(context.Background()); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor maps a RunE error onto the §8.1 exit-code set. kit's RunE
// middleware returns the *output.Error envelope it rendered, so its
// ExitCode is authoritative. A bare *exitError (no middleware in the
// path) and cobra's own flag/arg errors fall back sensibly.
func exitCodeFor(err error) int {
	var oe *output.Error
	if errors.As(err, &oe) && oe.ExitCode != 0 {
		return oe.ExitCode
	}
	var ee *exitError
	if errors.As(err, &ee) && ee.cli != nil && ee.cli.ExitCode != 0 {
		return ee.cli.ExitCode
	}
	// Cobra reports bad flags / too many args as a plain error before
	// our RunE runs; treat those as usage errors.
	return exitUsage
}

func printExtInfo(w io.Writer) error {
	info := extInfo{
		Name:         "youtube",
		Version:      version,
		Description:  "YouTube transcript and metadata extraction",
		Capabilities: []string{"discover"},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

func newRoot() *kitcli.Root {
	var (
		transcript bool
		timestamps bool
		comments   bool
		metadata   bool
		noCache    bool
		debug      bool
		raw        bool
	)

	root := kitcli.New(kitcli.Config{
		Name:    "foo-youtube",
		Version: version,
		Short:   "YouTube transcript and metadata extraction",
		Help: kitcli.HelpConfig{
			Disclaimer: `foo-youtube fetches a YouTube video's transcript and metadata
via yt-dlp and renders them as markdown on stdout.

Arguments:
  <url|id>  YouTube video URL (youtube.com/watch, youtu.be, or
            youtube.com/shorts), or a bare 11-character video ID
            such as dQw4w9WgXcQ. Required.
  <prompt>  Optional question to answer about the video. With one,
            the transcript is sent to a model and the answer is
            printed. Without one, the markdown itself is printed, so
            piping foo-youtube into foo keeps working.
            FOO_YOUTUBE_PROMPT, then FOO_PROMPT, supply a default;
            --raw suppresses both and always prints markdown.

It is an external plugin for foo: the host discovers it on $PATH and
interrogates it with --ext-info.`,
		},
	}, kitcli.WithStatus(kitcli.StatusConfig{}))

	root.Cmd.Use = "foo-youtube [flags] <url|id> [prompt]"
	root.Cmd.Args = cobra.MaximumNArgs(2)
	root.Cmd.SilenceUsage = true
	root.Cmd.SilenceErrors = true

	// Each negation binds its OWN variable. Binding --no-X to the same
	// pointer as --X writes the negation's default (false) through that
	// pointer at registration time, so both switches silently land on
	// false and a bare invocation does no work — while --help still
	// advertises the first registration's `true`. The RunE
	// reconciliation below is what actually applies a negation.
	var (
		noMetadata   bool
		noTranscript bool
	)

	flags := root.Cmd.Flags()
	flags.BoolVar(&metadata, "metadata", true, "Include video metadata")
	flags.BoolVar(&noMetadata, "no-metadata", false, "Skip video metadata")
	flags.BoolVar(&transcript, "transcript", true, "Extract transcript")
	flags.BoolVar(&noTranscript, "no-transcript", false, "Skip transcript extraction")
	flags.BoolVar(&timestamps, "timestamps", false, "Include timestamps in transcript")
	flags.BoolVar(&comments, "comments", false, "Include top comments")
	flags.BoolVar(&noCache, "no-cache", false, "Bypass the yt-dlp output cache for this run")
	flags.BoolVar(&raw, "raw", false, "Print the markdown even when a prompt is configured")
	// yt-dlp's own stderr is suppressed by default so it never mixes
	// with this sidecar's progress lines; --debug restores the raw
	// passthrough for diagnosis. -V/--verbose is kit-owned (log level),
	// so -v is free for this.
	flags.BoolVarP(&debug, "debug", "v", false, "Pass yt-dlp's raw stderr through for diagnosis")

	// --ext-info is registered for help/discoverability parity; the real
	// handling happens pre-cobra in main so the JSON contract stays
	// clean. Hidden because it is a host-facing probe, not a user verb.
	var extInfoFlag bool
	flags.BoolVar(&extInfoFlag, "ext-info", false, "Print extension info as JSON (used by the foo host)")
	_ = flags.MarkHidden("ext-info")

	root.Cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Paired negation: --no-X overrides the default-true switch.
		if cmd.Flags().Changed("no-metadata") {
			metadata = !boolFlag(cmd, "no-metadata")
		}
		if cmd.Flags().Changed("no-transcript") {
			transcript = !boolFlag(cmd, "no-transcript")
		}
		return runFunc(cmd, args, runOpts{
			metadata:   metadata,
			transcript: transcript,
			timestamps: timestamps,
			comments:   comments,
			noCache:    noCache,
			debug:      debug,
			raw:        raw,
		})
	}

	kitcli.SetSideEffect(root.Cmd, kitcli.SideEffectRead)
	kitcli.SetIdempotency(root.Cmd, kitcli.IdempotencyYes)
	return root
}

func boolFlag(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// runFunc is the seam between flag parsing and the extraction itself.
// It defaults to run; tests swap it to capture the runOpts the parsed
// flags actually produce, so flag registration and the paired-negation
// reconciliation are exercised as written rather than reimplemented.
var runFunc = run

type runOpts struct {
	metadata   bool
	transcript bool
	timestamps bool
	comments   bool
	noCache    bool
	debug      bool
	raw        bool
}

func run(cmd *cobra.Command, args []string, opts runOpts) error {
	if len(args) == 0 {
		return usageErrorf("YouTube URL required")
	}

	// Resolve the question before any fetching: --raw suppresses the
	// prompt path entirely so a configured FOO_YOUTUBE_PROMPT cannot
	// hijack a pipeline that wants the markdown. A second positional
	// outranks both env names; no prompt from any source leaves
	// question empty and the markdown goes to stdout as it always has.
	var promptArg string
	if len(args) > 1 {
		promptArg = args[1]
	}
	question := resolvePrompt(promptArg)
	if opts.raw {
		question = ""
	}
	// A prompt is a question about what the video says, so the
	// transcript is not optional on that path. Refuse rather than
	// silently answering from the metadata header alone.
	if question != "" && !opts.transcript {
		return usageErrorf("--no-transcript cannot answer a prompt; drop the prompt or drop --no-transcript")
	}

	// Normalize before anything downstream sees the argument: a bare
	// video ID becomes the canonical watch URL here, so metadata,
	// transcript, comments and the yt-dlp cache key all observe the same
	// string for both input forms.
	url, ok := normalizeVideoArg(args[0])
	if !ok {
		return usageErrorf("invalid YouTube URL: %s", args[0])
	}

	// Start the telemetry clock and set the raw-passthrough gate before
	// any yt-dlp work runs.
	started = time.Now()
	ytDebug = opts.debug

	if err := checkYTDLP(); err != nil {
		return missingDepError(err)
	}

	// Open the yt-dlp output cache unless the run opted out. Best-effort:
	// a failed open leaves ytCache nil and runYTDLP execs directly.
	ctx := cmd.Context()
	if !opts.noCache {
		openYTCache()
	}

	// Wire the event bus once the request is validated and the dependency
	// is present, before any extraction runs. A failed fetch below
	// returns early and publishes nothing.
	if eventBus == nil {
		eventBus = kitbus.New()
		wireBusNetwork(ctx)
	}

	var md *videoMetadata
	if opts.metadata {
		var err error
		md, err = fetchMetadata(ctx, url)
		if err != nil {
			return fetchErrorf("fetching metadata: %v", err)
		}
		publishEvent(ctx, "foo-youtube.capture.metadata.fetched", map[string]any{"url": url})
	}

	var transcriptText string
	if opts.transcript {
		var err error
		transcriptText, err = fetchTranscript(ctx, url, opts.timestamps)
		if err != nil {
			return fetchErrorf("fetching transcript: %v", err)
		}
		publishEvent(ctx, "foo-youtube.capture.transcript.fetched", map[string]any{"url": url})
	}

	var commentList []comment
	if opts.comments {
		var err error
		commentList, err = fetchComments(ctx, url)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not fetch comments: %v\n", err)
			// Non-fatal: continue without comments.
		}
	}

	// Render to a buffer first so the completion event can report the
	// payload's size and token estimate, then write it to stdout in one
	// go. Stdout carries the markdown and nothing else; every progress
	// line went to the reporter's stderr stream.
	var buf bytes.Buffer
	renderMarkdown(&buf, md, transcriptText, commentList)

	// With a question, the markdown is the model's input rather than
	// the user's output; stdout then carries the answer alone so it
	// stays as pipeable as the markdown was.
	payload := buf.Bytes()
	if question != "" {
		reply, err := answerFunc(ctx, resolveModel(), buildPrompt(question, buf.String()))
		if err != nil {
			return promptError(err)
		}
		payload = []byte(strings.TrimRight(reply, "\n") + "\n")
	}

	if _, err := cmd.OutOrStdout().Write(payload); err != nil {
		return fetchErrorf("writing output: %v", err)
	}

	emitDone(ctx, url, int64(len(payload)), estimateTokens(string(payload)))
	return nil
}

// promptError maps a completion failure onto the §8.1 exit-code set. An
// error that already carries an envelope (the missing-key precheck)
// keeps its own code; anything the provider raised — including a
// context-window overflow on a long video — surfaces verbatim under the
// generic code rather than being reinterpreted here.
func promptError(err error) error {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee
	}
	return fetchErrorf("answering prompt: %v", err)
}

// wireBusNetwork attaches a NetworkAdapter to the in-process bus so the
// capture events foo-youtube publishes reach external subscribers (aps,
// ctxt, tlc) over WebSocket. A bare bus.New() publishes to nobody; the
// adapter subscribes to every local topic and forwards to each peer.
//
// Peers are read from FOO_YOUTUBE_BUS_PEERS (comma-separated ws:// URLs);
// with none set, the adapter is skipped and events stay in-process.
// Connects are best-effort: a failure is logged and never fatal. An auth
// token from FOO_BUS_TOKEN / BUS_TOKEN is attached when present, sharing
// the host's token names.
func wireBusNetwork(ctx context.Context) {
	if eventBus == nil {
		return
	}
	raw := strings.TrimSpace(os.Getenv("FOO_YOUTUBE_BUS_PEERS"))
	if raw == "" {
		return
	}
	var opts []kitbus.NetworkOption
	if auth, ok := kitbus.AuthFromEnv("FOO_BUS_TOKEN", "BUS_TOKEN"); ok {
		opts = append(opts, kitbus.WithAuth(auth))
	}
	busNet = kitbus.NewNetworkAdapter(eventBus, opts...)
	for _, addr := range strings.Split(raw, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if err := busNet.Connect(ctx, addr); err != nil {
			slog.Warn("bus.network.connect.failed", slog.String("addr", addr), slog.Any("err", err))
		}
	}
}

// publishEvent emits one capture event onto the bus, tolerating a nil
// bus (no-op). The source segment is the binary name so subscribers can
// filter foo-youtube traffic from the host and sibling sidecars.
func publishEvent(ctx context.Context, topic string, payload any) {
	if eventBus == nil {
		return
	}
	_ = eventBus.Publish(ctx, kitbus.NewEvent(kitbus.Topic(topic), "foo-youtube", payload))
}

func isYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com/") ||
		strings.Contains(url, "youtu.be/") ||
		strings.Contains(url, "youtube.com/shorts/")
}

// videoIDPattern is the canonical YouTube video ID shape: exactly eleven
// characters drawn from the URL-safe base64 alphabet. Ten- and
// twelve-character strings are deliberately outside it.
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// isVideoID reports whether s is a bare YouTube video ID.
func isVideoID(s string) bool { return videoIDPattern.MatchString(s) }

// normalizeVideoArg resolves the sole positional argument to a URL
// yt-dlp can consume. A supported YouTube URL is returned unchanged; a
// bare video ID is expanded to its canonical watch URL. Anything else
// yields ok=false, which run() reports as the usage error.
func normalizeVideoArg(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	switch {
	case isYouTubeURL(arg):
		return arg, true
	case isVideoID(arg):
		return "https://www.youtube.com/watch?v=" + arg, true
	default:
		return "", false
	}
}

func checkYTDLP() error {
	_, err := exec.LookPath("yt-dlp")
	if err != nil {
		return fmt.Errorf("yt-dlp not found in PATH; install it: https://github.com/yt-dlp/yt-dlp")
	}
	return nil
}

// openYTCache initializes the package-level yt-dlp output cache. The db
// path and TTL resolve from env (see resolveCachePath / resolveCacheTTL):
// this extension's FOO_YOUTUBE_CACHE(_TTL), else the host-level
// FOO_CACHE(_TTL) it inherits, else the XDG cache dir at 24h.
//
// A resolved TTL of zero means caching OFF: the store is never opened,
// which is the same path as --no-cache. Zero must not reach the
// PutWithTTL/Put branch in runYTDLP as "no expiry" — an off switch that
// caches forever is the opposite of off.
//
// Best-effort otherwise: any failure logs at warn and leaves ytCache nil
// so runYTDLP execs directly — caching is an optimization, never a hard
// dependency.
func openYTCache() {
	ttl := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL", youtubeCacheTTLDefault)
	if ttl <= 0 {
		return
	}
	ytCacheTTL = ttl

	path, err := resolveCachePath("foo-youtube", youtubeCacheDB, "FOO_YOUTUBE_CACHE")
	if err != nil {
		slog.Warn("youtube.cache.path.failed", slog.Any("err", err))
		return
	}

	store, err := kv.Open(kv.Config{Backend: "sqlite", Path: path})
	if err != nil {
		slog.Warn("youtube.cache.open.failed", slog.String("path", path), slog.Any("err", err))
		return
	}
	ttlStore, ok := store.(kv.TTLStore)
	if !ok {
		_ = store.Close()
		return
	}
	ytCache = ttlStore
}

// resolveCachePath picks the cache db path with this precedence:
// FOO_<EXT>_CACHE (here FOO_YOUTUBE_CACHE) → FOO_CACHE → the XDG cache dir
// for tool.
//
// The namespace is deliberate: the unprefixed FOO_CACHE is foo's own
// host-level cache setting, which every extension inherits as its
// default; the FOO_<EXT>_ prefixed form belongs to one extension and
// overrides it. Any future extension follows the same two names.
// (The host binary has no cache of its own yet, so today FOO_CACHE only
// ever takes effect through an extension reading it here.)
//
// The extension env names the db file outright; FOO_CACHE names a
// directory, under which dbName is joined so sibling extensions keep
// distinct stores. kv.Open creates a missing parent directory, so no
// MkdirAll is needed here.
//
// Signature is kept identical to foo-scrape's so the two sidecars resolve
// by the same rules — these are separate main packages, so the
// duplication is structural, but the shape must not drift.
func resolveCachePath(tool, dbName, specificEnv string) (string, error) {
	if p := strings.TrimSpace(os.Getenv(specificEnv)); p != "" {
		return p, nil
	}
	if dir := strings.TrimSpace(os.Getenv("FOO_CACHE")); dir != "" {
		return filepath.Join(dir, dbName), nil
	}
	return xdg.CacheFile(tool, dbName)
}

// resolveCacheTTL reads FOO_<EXT>_CACHE_TTL (here FOO_YOUTUBE_CACHE_TTL),
// falling back to foo's host-level FOO_CACHE_TTL, then to def — the
// same host-inherits-to-extension namespace as resolveCachePath.
// Values are Go duration strings ("24h", "90m"); an unparseable
// value falls through to the next source.
//
// A parsed zero ("0", "0s") is honored and means caching OFF — callers
// must treat a non-positive result as "skip the cache", never as an
// entry that never expires.
func resolveCacheTTL(specificEnv string, def time.Duration) time.Duration {
	for _, env := range []string{specificEnv, "FOO_CACHE_TTL"} {
		if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(env))); err == nil {
			return d
		}
	}
	return def
}

// runYTDLP execs `yt-dlp <args>` and returns its stdout. When the cache
// is enabled, output is keyed by the full argv: a hit returns the stored
// bytes without spawning yt-dlp, a miss execs and stores the result.
// Caching is best-effort — a read/decode failure degrades to a fresh
// exec, and a store-write failure is swallowed. Each call reports a
// fetch start and its cache outcome on the progress reporter; yt-dlp's
// own stderr is suppressed unless --debug is set (see execYTDLP).
func runYTDLP(ctx context.Context, args []string) ([]byte, error) {
	item := ytProgressItem(args)
	emitFetchStart(ctx, item)

	key := ytCacheKey(args)
	if ytCache != nil {
		if raw, ok, err := ytCache.Get(ctx, key); err == nil && ok {
			emitCacheOutcome(ctx, item, true, len(raw))
			return raw, nil
		}
	}

	out, err := ytRunner(ctx, args)
	if err != nil {
		return nil, err
	}
	emitCacheOutcome(ctx, item, false, len(out))

	if ytCache != nil {
		if ytCacheTTL > 0 {
			_ = ytCache.PutWithTTL(ctx, key, out, ytCacheTTL)
		} else {
			_ = ytCache.Put(ctx, key, out)
		}
	}
	return out, nil
}

// ytRunner is the single seam through which yt-dlp is executed. It
// defaults to a real subprocess; tests swap it for an xrr-backed runner
// so the suite replays recorded yt-dlp output instead of shelling out.
var ytRunner = execYTDLP

// ytDebug gates raw yt-dlp stderr passthrough. Set from --debug/-v in
// run(); false by default so yt-dlp's progress bars and diagnostics
// never mix with this sidecar's own progress lines.
var ytDebug bool

// ytStderr is where raw yt-dlp stderr goes when ytDebug is set. A
// package var so tests can capture it instead of the process stderr.
var ytStderr io.Writer = os.Stderr

// execYTDLP runs the real yt-dlp subprocess and returns its stdout.
//
// By default yt-dlp's stderr is captured rather than streamed: its
// progress output is verbose, unstructured, and duplicates the
// progress events this sidecar emits. Capturing is not swallowing —
// on a non-zero exit the captured text is folded into the returned
// error so a failing yt-dlp still explains itself. With --debug the
// stream is passed through verbatim for diagnosis.
func execYTDLP(_ context.Context, args []string) ([]byte, error) {
	cmd := exec.Command("yt-dlp", args...)
	if ytDebug {
		cmd.Stderr = ytStderr
		return cmd.Output()
	}

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(errBuf.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

// ytProgressItem picks the human-facing label for a yt-dlp invocation:
// the URL argument when present, else the first flag. Keeps progress
// lines readable without echoing the whole argv.
func ytProgressItem(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
			return a
		}
	}
	if len(args) > 0 {
		return args[0]
	}
	return "yt-dlp"
}

// ytCacheKey derives a stable key from the yt-dlp argv. The "yt-dlp\x00"
// prefix and NUL separators keep distinct argv from colliding.
func ytCacheKey(args []string) string {
	h := sha256.New()
	h.Write([]byte("yt-dlp\x00"))
	for _, a := range args {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	return "foo-youtube:" + hex.EncodeToString(h.Sum(nil))
}

func fetchMetadata(ctx context.Context, url string) (*videoMetadata, error) {
	out, err := runYTDLP(ctx, []string{
		"--dump-json",
		"--no-download",
		"--no-playlist",
		url,
	})
	if err != nil {
		return nil, fmt.Errorf("yt-dlp metadata: %w", err)
	}

	var md videoMetadata
	if err := json.Unmarshal(out, &md); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	return &md, nil
}

// errNoTranscript reports a video yt-dlp fetched successfully but which
// carries no English subtitle track. It is a real failure, not an empty
// section: a silently empty transcript exits 0 with nothing on stdout,
// which downstream reads as "summarize this" with an empty prompt. The
// caller wraps it into the exit-1 fetch error so the operator learns the
// video has no captions instead of receiving a blank document.
var errNoTranscript = errors.New("no English transcript available for this video")

// transcriptSubLang is the subtitle language yt-dlp is asked for.
//
// Exactly "en", never a pattern. yt-dlp anchors --sub-lang as a regex,
// so "en" matches the English track alone, while "en.*" would also pull
// en-en and the ~1.2 MB en-orig track, and a bare language prefix would
// drag in the hundreds of <lang>-en machine translations this video
// exposes. Selecting the wrong one silently yields a translated or
// duplicated transcript, so the exact tag is load-bearing.
const transcriptSubLang = "en"

// fetchTranscript downloads the English subtitle track and renders it as
// text.
//
// yt-dlp has no "subtitles on stdout" mode: -o is its output *template*,
// so `-o -` does not stream — it writes a file literally named "-.en.json3"
// into the process's working directory and leaves stdout empty. This
// downloads into a temp dir with a real template instead, reads the
// produced .json3 back, and removes the directory afterwards, so nothing
// is left in the user's cwd.
func fetchTranscript(ctx context.Context, url string, withTimestamps bool) (string, error) {
	dir, err := os.MkdirTemp("", "foo-youtube-subs-")
	if err != nil {
		return "", fmt.Errorf("subtitle temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The output template is deliberately id-only and extension-free:
	// yt-dlp appends ".<lang>.<format>" itself, and keeping the video
	// title out of the name avoids filesystem-hostile characters.
	if _, err := runYTDLP(ctx, []string{
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-lang", transcriptSubLang,
		"--sub-format", "json3",
		"--no-playlist",
		"-o", filepath.Join(dir, "%(id)s"),
		url,
	}); err != nil {
		return "", fmt.Errorf("yt-dlp transcript: %w", err)
	}

	data, err := readSubtitleFile(dir)
	if err != nil {
		return "", err
	}

	text, err := parseTranscript(data, withTimestamps)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errNoTranscript
	}
	return text, nil
}

// readSubtitleFile returns the contents of the English json3 subtitle
// file yt-dlp wrote into dir.
//
// A successful yt-dlp exit with no subtitle file means the video has no
// English captions — yt-dlp reports that on stderr and still exits 0, so
// the missing file is the only signal available here. That is
// errNoTranscript, never an empty string: the whole point of this path
// is that "nothing" must not reach stdout as a silent success.
func readSubtitleFile(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading subtitle dir: %w", err)
	}

	// Prefer the exact ".<lang>.json3" suffix. yt-dlp is asked for one
	// language, but pinning the suffix keeps a future multi-track change
	// from picking an auto-translation by directory order.
	want := "." + transcriptSubLang + ".json3"
	var match string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), want) {
			match = e.Name()
			break
		}
	}
	if match == "" {
		return nil, errNoTranscript
	}

	data, err := os.ReadFile(filepath.Join(dir, match))
	if err != nil {
		return nil, fmt.Errorf("reading subtitle file: %w", err)
	}
	return data, nil
}

type json3Transcript struct {
	Events []json3Event `json:"events"`
}

type json3Event struct {
	TStartMs int        `json:"tStartMs"`
	Segs     []json3Seg `json:"segs"`
}

type json3Seg struct {
	UTF8 string `json:"utf8"`
}

func parseTranscript(data []byte, withTimestamps bool) (string, error) {
	var t json3Transcript
	if err := json.Unmarshal(data, &t); err != nil {
		// If not JSON, return as plain text
		return strings.TrimSpace(string(data)), nil
	}

	var sb strings.Builder
	for _, ev := range t.Events {
		if len(ev.Segs) == 0 {
			continue
		}

		var line string
		for _, seg := range ev.Segs {
			line += seg.UTF8
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if withTimestamps {
			ts := formatTimestamp(ev.TStartMs)
			sb.WriteString(fmt.Sprintf("[%s] %s\n", ts, line))
		} else {
			sb.WriteString(line + " ")
		}
	}

	return strings.TrimSpace(sb.String()), nil
}

func formatTimestamp(ms int) string {
	totalSec := ms / 1000
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func fetchComments(ctx context.Context, url string) ([]comment, error) {
	out, err := runYTDLP(ctx, []string{
		"--dump-json",
		"--no-download",
		"--write-comments",
		"--no-playlist",
		"--extractor-args", "youtube:max_comments=20",
		url,
	})
	if err != nil {
		return nil, fmt.Errorf("yt-dlp comments: %w", err)
	}

	// yt-dlp embeds comments in the info JSON
	var info struct {
		Comments []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("parse comments: %w", err)
	}

	var result []comment
	for _, c := range info.Comments {
		result = append(result, comment{
			Author: c.Author,
			Text:   c.Text,
		})
	}

	return result, nil
}

func renderMarkdown(w io.Writer, md *videoMetadata, transcript string, comments []comment) {
	if md != nil {
		title := md.Title
		if title == "" {
			title = "Untitled Video"
		}
		fmt.Fprintf(w, "# %s\n\n", title)

		fmt.Fprintln(w, "## Metadata")
		if md.Channel != "" {
			fmt.Fprintf(w, "- **Channel:** %s\n", md.Channel)
		}
		if md.UploadDate != "" {
			fmt.Fprintf(w, "- **Published:** %s\n", formatDate(md.UploadDate))
		}
		if md.Duration > 0 {
			fmt.Fprintf(w, "- **Duration:** %s\n", formatDuration(md.Duration))
		}
		if md.ViewCount > 0 {
			fmt.Fprintf(w, "- **Views:** %d\n", md.ViewCount)
		}
		if md.LikeCount > 0 {
			fmt.Fprintf(w, "- **Likes:** %d\n", md.LikeCount)
		}
		fmt.Fprintln(w)
	}

	if transcript != "" {
		fmt.Fprintln(w, "## Transcript")
		fmt.Fprintln(w)
		fmt.Fprintln(w, transcript)
		fmt.Fprintln(w)
	}

	if len(comments) > 0 {
		fmt.Fprintln(w, "## Comments")
		fmt.Fprintln(w)
		for _, c := range comments {
			fmt.Fprintf(w, "- **%s:** %s\n", c.Author, c.Text)
		}
		fmt.Fprintln(w)
	}
}

func formatDate(yyyymmdd string) string {
	if len(yyyymmdd) == 8 {
		return yyyymmdd[:4] + "-" + yyyymmdd[4:6] + "-" + yyyymmdd[6:]
	}
	return yyyymmdd
}

func formatDuration(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}
