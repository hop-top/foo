// foo-youtube is a standalone binary that extracts a YouTube video's
// transcript and metadata as markdown. It is an external plugin for
// foo: the host discovers it on $PATH and interrogates it via
// --ext-info (kit ai/ext/discover). It imports zero foo internal
// packages.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/xdg"
	kitbus "hop.top/kit/go/runtime/bus"
	"hop.top/kit/go/storage/kv"
)

var version = "dev"

// ytCache caches raw yt-dlp invocation output keyed by argv, so repeated
// extractions of the same video skip the subprocess + network. nil when
// caching is disabled (--no-cache or a store-open failure); runYTDLP
// tolerates the nil cache and just execs. Initialized once in run().
var ytCache kv.TTLStore

// ytCacheTTL is the freshness window for cached yt-dlp output. Overridable
// via FOO_YOUTUBE_CACHE_TTL (a Go duration); default 24h.
var ytCacheTTL = 24 * time.Hour

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

func printExtInfo(w *os.File) error {
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
	)

	root := kitcli.New(kitcli.Config{
		Name:    "foo-youtube",
		Version: version,
		Short:   "YouTube transcript and metadata extraction",
		Help: kitcli.HelpConfig{
			Disclaimer: `foo-youtube fetches a YouTube video's transcript and metadata
via yt-dlp and renders them as markdown on stdout.

Arguments:
  <url>   YouTube video URL (youtube.com/watch, youtu.be, or
          youtube.com/shorts). Required.

It is an external plugin for foo: the host discovers it on $PATH and
interrogates it with --ext-info.`,
		},
	}, kitcli.WithStatus(kitcli.StatusConfig{}))

	root.Cmd.Use = "foo-youtube [flags] <url>"
	root.Cmd.Args = cobra.MaximumNArgs(1)
	root.Cmd.SilenceUsage = true
	root.Cmd.SilenceErrors = true

	flags := root.Cmd.Flags()
	flags.BoolVar(&metadata, "metadata", true, "Include video metadata")
	flags.BoolVar(&metadata, "no-metadata", false, "Skip video metadata")
	flags.BoolVar(&transcript, "transcript", true, "Extract transcript")
	flags.BoolVar(&transcript, "no-transcript", false, "Skip transcript extraction")
	flags.BoolVar(&timestamps, "timestamps", false, "Include timestamps in transcript")
	flags.BoolVar(&comments, "comments", false, "Include top comments")
	flags.BoolVar(&noCache, "no-cache", false, "Bypass the yt-dlp output cache for this run")

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
		return run(cmd, args, runOpts{
			metadata:   metadata,
			transcript: transcript,
			timestamps: timestamps,
			comments:   comments,
			noCache:    noCache,
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

type runOpts struct {
	metadata   bool
	transcript bool
	timestamps bool
	comments   bool
	noCache    bool
}

func run(cmd *cobra.Command, args []string, opts runOpts) error {
	if len(args) == 0 {
		return usageErrorf("YouTube URL required")
	}

	url := args[0]
	if !isYouTubeURL(url) {
		return usageErrorf("invalid YouTube URL: %s", url)
	}

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

	out, ok := cmd.OutOrStdout().(*os.File)
	if !ok {
		out = os.Stdout
	}
	renderMarkdown(out, md, transcriptText, commentList)
	return nil
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

func checkYTDLP() error {
	_, err := exec.LookPath("yt-dlp")
	if err != nil {
		return fmt.Errorf("yt-dlp not found in PATH; install it: https://github.com/yt-dlp/yt-dlp")
	}
	return nil
}

// openYTCache initializes the package-level yt-dlp output cache. The db
// path defaults to the XDG cache dir (FOO_YOUTUBE_CACHE overrides it),
// and FOO_YOUTUBE_CACHE_TTL overrides the freshness window. Best-effort:
// any failure logs at warn and leaves ytCache nil so runYTDLP execs
// directly — caching is an optimization, never a hard dependency.
func openYTCache() {
	ytCacheTTL = resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL")

	path, err := resolveCachePath("foo-youtube", "ytdlp-cache.db", "FOO_YOUTUBE_CACHE")
	if err != nil {
		slog.Warn("youtube.cache.path.failed", slog.Any("err", err))
		return
	}

	store, err := kv.Open(kv.Config{Backend: "sqlite", Path: path})
	if err != nil {
		slog.Warn("youtube.cache.open.failed", slog.String("path", path), slog.Any("err", err))
		return
	}
	ttl, ok := store.(kv.TTLStore)
	if !ok {
		_ = store.Close()
		return
	}
	ytCache = ttl
}

// resolveCachePath picks the cache db path with this precedence: the
// plugin-specific env (FOO_YOUTUBE_CACHE) → the shared FOO_CACHE → the
// XDG cache dir under tool. A shared FOO_CACHE value is treated as a
// directory and dbName is joined under it, so plugins sharing FOO_CACHE
// keep distinct files.
func resolveCachePath(tool, dbName, specificEnv string) (string, error) {
	if p := strings.TrimSpace(os.Getenv(specificEnv)); p != "" {
		return p, nil
	}
	if dir := strings.TrimSpace(os.Getenv("FOO_CACHE")); dir != "" {
		return filepath.Join(dir, dbName), nil
	}
	return xdg.CacheFile(tool, dbName)
}

// resolveCacheTTL reads the plugin-specific TTL env, falling back to the
// shared FOO_CACHE_TTL, then to 24h. An unparseable value falls through
// to the next source.
func resolveCacheTTL(specificEnv string) time.Duration {
	for _, env := range []string{specificEnv, "FOO_CACHE_TTL"} {
		if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(env))); err == nil {
			return d
		}
	}
	return 24 * time.Hour
}

// runYTDLP execs `yt-dlp <args>` and returns its stdout. When the cache
// is enabled, output is keyed by the full argv: a hit returns the stored
// bytes without spawning yt-dlp, a miss execs and stores the result.
// Caching is best-effort — a read/decode failure degrades to a fresh
// exec, and a store-write failure is swallowed. stderr always streams to
// the process stderr so yt-dlp diagnostics surface on both paths.
func runYTDLP(ctx context.Context, args []string) ([]byte, error) {
	key := ytCacheKey(args)
	if ytCache != nil {
		if raw, ok, err := ytCache.Get(ctx, key); err == nil && ok {
			return raw, nil
		}
	}

	out, err := ytRunner(ctx, args)
	if err != nil {
		return nil, err
	}

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

// execYTDLP runs the real yt-dlp subprocess, streaming its stderr so
// diagnostics surface to the user, and returns stdout.
func execYTDLP(_ context.Context, args []string) ([]byte, error) {
	cmd := exec.Command("yt-dlp", args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
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

func fetchTranscript(ctx context.Context, url string, withTimestamps bool) (string, error) {
	out, err := runYTDLP(ctx, []string{
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-lang", "en",
		"--sub-format", "json3",
		"--no-playlist",
		"-o", "-",
		url,
	})
	if err != nil {
		// Fallback: try getting subtitles via different approach
		return fetchTranscriptFallback(ctx, url, withTimestamps)
	}

	return parseTranscript(out, withTimestamps)
}

func fetchTranscriptFallback(ctx context.Context, url string, withTimestamps bool) (string, error) {
	// Use yt-dlp to get subtitle file
	out, err := runYTDLP(ctx, []string{
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-lang", "en",
		"--sub-format", "vtt",
		"--print", "subtitle",
		"--no-playlist",
		url,
	})
	if err != nil {
		return "", fmt.Errorf("yt-dlp transcript: %w", err)
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("no transcript available for this video")
	}

	return text, nil
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

func renderMarkdown(w *os.File, md *videoMetadata, transcript string, comments []comment) {
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
