// foo-scrape is a standalone binary that converts URLs to markdown.
// It is an external plugin for foo and imports zero foo internal packages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/net/html"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
	kitbus "hop.top/kit/go/runtime/bus"
)

var version = "dev"

// eventBus is the in-process pub/sub bus this sidecar publishes capture
// events to. A NetworkAdapter (wired in newRoot) forwards local topics to
// configured peers so sibling tools (aps, ctxt, tlc) observe a successful
// scrape. nil-guarded everywhere: an unwired bus publishes to nobody and
// never fails the scrape.
var (
	eventBus kitbus.Bus
	busNet   *kitbus.NetworkAdapter
)

// extInfo is the discovery contract the host foo binary parses via
// kit's ai/ext/discover. The four fields (name, version, description,
// capabilities) are a hard wire contract — keep them stable.
type extInfo struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

func main() {
	// --ext-info is a hard wire contract: the host discovers this
	// sidecar by executing it with --ext-info and parsing the JSON
	// below. Intercept before cobra so discovery never depends on
	// arg-count validation or flag parsing succeeding.
	if hasExtInfo(os.Args[1:]) {
		emitExtInfo()
		return
	}

	root := newRoot()
	if err := root.Execute(context.Background()); err != nil {
		os.Exit(exitCode(err))
	}
}

// exitCode maps a returned error to a process exit code. Errors that
// carry a kit error envelope (usage errors → 2, etc.) honor the
// embedded code; everything else is a generic failure (1).
func exitCode(err error) int {
	var ce interface{ AsCLIError() *output.Error }
	if errors.As(err, &ce) {
		if env := ce.AsCLIError(); env != nil && env.ExitCode != 0 {
			return env.ExitCode
		}
	}
	return 1
}

// hasExtInfo reports whether --ext-info appears anywhere in args.
func hasExtInfo(args []string) bool {
	for _, a := range args {
		if a == "--ext-info" {
			return true
		}
	}
	return false
}

// emitExtInfo prints the discovery JSON and is the only path that
// must keep emitting exactly the four-field contract.
func emitExtInfo() {
	info := extInfo{
		Name:         "scrape",
		Version:      version,
		Description:  "URL to markdown conversion with readability",
		Capabilities: []string{"discover"},
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(info)
}

// scrapeModes is the closed set --mode accepts.
var scrapeModes = []string{"readability", "raw"}

func newRoot() *kitcli.Root {
	var mode string

	root := kitcli.New(kitcli.Config{
		Name:    "foo-scrape",
		Version: version,
		Short:   "Convert a URL to markdown",
		// Single-file sidecar: no subcommands, no status command, so
		// the leaf/status validators don't apply. Annotations below
		// keep the side-effect/idempotency contract declared for any
		// downstream consumer that does inspect them.
		DisableValidate: true,
	})

	root.Cmd.Use = "foo-scrape [flags] <url>"
	root.Cmd.Long = `foo-scrape fetches a URL and converts the page to markdown.

With --mode readability (default) it extracts the main article content
and drops navigation, scripts, and chrome. With --mode raw it converts
the full HTML document. Use --ext-info to print discovery metadata as
JSON.`
	// Usage errors (bad flag, wrong arg count) carry exit code 2 per
	// the cross-tool exit-code table; main reads the embedded code.
	root.Cmd.Args = usageArgs(cobra.ExactArgs(1))
	root.Cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return output.UsageError(err.Error())
	})
	root.Cmd.SilenceUsage = true
	root.Cmd.SilenceErrors = true
	root.Cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !slices.Contains(scrapeModes, mode) {
			return output.UsageError(fmt.Sprintf(
				"invalid --mode %q: must be one of %s",
				mode, strings.Join(scrapeModes, ", ")))
		}
		// Construct the in-process bus + network adapter once, lazily, so
		// --ext-info and usage errors never touch network. A failed
		// scrape publishes nothing; the publish call lives at the tail of
		// scrape() past every error return.
		if eventBus == nil {
			eventBus = kitbus.New()
			wireBusNetwork(cmd.Context())
		}
		return scrape(cmd, args[0], mode)
	}

	flags := root.Cmd.Flags()
	flags.StringVar(&mode, "mode", "readability",
		"Conversion mode: readability (main content) or raw (full HTML)")

	// Side-effect / idempotency contract: a fetch-and-print is a pure
	// read, trivially idempotent against the same URL.
	kitcli.SetSideEffect(root.Cmd, kitcli.SideEffectRead)
	kitcli.SetIdempotency(root.Cmd, kitcli.IdempotencyYes)

	return root
}

// usageArgs wraps a cobra positional-args validator so a failure
// surfaces as a kit usage error (exit code 2) instead of a generic
// exit-1 error.
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return output.UsageError(err.Error())
		}
		return nil
	}
}

// scrape fetches url and writes the converted markdown to the command's
// stdout. mode is "readability" or "raw".
func scrape(cmd *cobra.Command, url, mode string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return fmt.Errorf("parsing HTML: %w", err)
	}

	out := cmd.OutOrStdout()
	if mode == "raw" {
		md := convertNode(doc, false)
		_, _ = fmt.Fprint(out, cleanMarkdown(md))
		publishScraped(cmd.Context(), url, mode)
		return nil
	}

	title := extractTitle(doc)
	content := extractArticleContent(doc)
	if title != "" {
		_, _ = fmt.Fprintf(out, "# %s\n\n", title)
	}
	md := convertNode(content, true)
	_, _ = fmt.Fprint(out, cleanMarkdown(md))
	publishScraped(cmd.Context(), url, mode)
	return nil
}

// wireBusNetwork attaches a NetworkAdapter to the in-process bus so the
// capture events this sidecar publishes reach external subscribers (aps,
// ctxt, tlc) over WebSocket. A bare bus.New() publishes to nobody; the
// adapter forwards every local topic to each connected peer.
//
// Peers are read from FOO_SCRAPE_BUS_PEERS (comma-separated ws:// URLs);
// with no peers configured the adapter is skipped and events stay
// in-process. An auth token from FOO_BUS_TOKEN / BUS_TOKEN is attached
// when present. Connects are best-effort: a failure is logged at warn
// and never fails the scrape (the sidecar has no --offline flag).
func wireBusNetwork(ctx context.Context) {
	if eventBus == nil {
		return
	}
	raw := strings.TrimSpace(os.Getenv("FOO_SCRAPE_BUS_PEERS"))
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

// publishScraped emits the capture event for one successful scrape. It is
// nil-guarded so an unwired bus is a no-op, and called only past every
// error return in scrape() — a failed scrape publishes nothing.
func publishScraped(ctx context.Context, url, mode string) {
	if eventBus == nil {
		return
	}
	payload := map[string]any{"url": url, "mode": mode}
	_ = eventBus.Publish(ctx, kitbus.NewEvent(
		kitbus.Topic("foo-scrape.capture.page.scraped"), "foo-scrape", payload))
}

// extractTitle finds the <title> or first <h1> in the document.
func extractTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" {
		return textContent(n)
	}
	if n.Type == html.ElementNode && n.Data == "h1" {
		return textContent(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := extractTitle(c); t != "" {
			return t
		}
	}
	return ""
}

// extractArticleContent finds the best candidate for main content.
// It looks for <article>, <main>, or a <div> with common content
// class/id patterns. Falls back to <body>.
func extractArticleContent(doc *html.Node) *html.Node {
	// Priority order: article > main > content div > body
	if n := findElement(doc, "article"); n != nil {
		return n
	}
	if n := findElement(doc, "main"); n != nil {
		return n
	}
	if n := findByAttr(doc, contentPatterns); n != nil {
		return n
	}
	if n := findElement(doc, "body"); n != nil {
		return n
	}
	return doc
}

var contentPatterns = []string{
	"content", "post", "entry", "article-body",
	"post-content", "entry-content", "article-content",
	"main-content", "page-content",
}

// findElement finds the first element with the given tag name.
func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// findByAttr finds a div whose id or class contains a content pattern.
func findByAttr(n *html.Node, patterns []string) *html.Node {
	if n.Type == html.ElementNode && n.Data == "div" {
		for _, a := range n.Attr {
			if a.Key == "id" || a.Key == "class" {
				val := strings.ToLower(a.Val)
				for _, p := range patterns {
					if strings.Contains(val, p) {
						return n
					}
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByAttr(c, patterns); found != nil {
			return found
		}
	}
	return nil
}

// skipTags are elements stripped in readability mode.
var skipTags = map[string]bool{
	"script": true, "style": true, "nav": true,
	"footer": true, "aside": true, "header": true,
	"noscript": true, "iframe": true, "form": true,
}

// convertNode converts an HTML node tree to markdown.
func convertNode(n *html.Node, readability bool) string {
	if n == nil {
		return ""
	}

	var sb strings.Builder
	convertRecursive(&sb, n, readability, 0, false)
	return sb.String()
}

func convertRecursive(
	sb *strings.Builder,
	n *html.Node,
	readability bool,
	listDepth int,
	inPre bool,
) {
	switch n.Type {
	case html.TextNode:
		text := n.Data
		if !inPre {
			text = collapseWhitespace(text)
		}
		sb.WriteString(text)
		return

	case html.ElementNode:
		tag := n.Data

		if readability && skipTags[tag] {
			return
		}

		switch tag {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(tag[1] - '0')
			sb.WriteString("\n\n")
			sb.WriteString(strings.Repeat("#", level))
			sb.WriteString(" ")
			writeChildren(sb, n, readability, listDepth, false)
			sb.WriteString("\n\n")
			return

		case "p":
			sb.WriteString("\n\n")
			writeChildren(sb, n, readability, listDepth, false)
			sb.WriteString("\n\n")
			return

		case "br":
			sb.WriteString("\n")
			return

		case "a":
			href := getAttr(n, "href")
			sb.WriteString("[")
			writeChildren(sb, n, readability, listDepth, false)
			sb.WriteString("](")
			sb.WriteString(href)
			sb.WriteString(")")
			return

		case "img":
			alt := getAttr(n, "alt")
			src := getAttr(n, "src")
			sb.WriteString("![")
			sb.WriteString(alt)
			sb.WriteString("](")
			sb.WriteString(src)
			sb.WriteString(")")
			return

		case "strong", "b":
			sb.WriteString("**")
			writeChildren(sb, n, readability, listDepth, false)
			sb.WriteString("**")
			return

		case "em", "i":
			sb.WriteString("*")
			writeChildren(sb, n, readability, listDepth, false)
			sb.WriteString("*")
			return

		case "code":
			if !inPre {
				sb.WriteString("`")
				writeChildren(sb, n, readability, listDepth, false)
				sb.WriteString("`")
			} else {
				writeChildren(sb, n, readability, listDepth, true)
			}
			return

		case "pre":
			sb.WriteString("\n\n```\n")
			writeChildren(sb, n, readability, listDepth, true)
			sb.WriteString("\n```\n\n")
			return

		case "ul":
			sb.WriteString("\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.Data == "li" {
					sb.WriteString(strings.Repeat("  ", listDepth))
					sb.WriteString("- ")
					writeChildren(sb, c, readability, listDepth+1, false)
					sb.WriteString("\n")
				}
			}
			sb.WriteString("\n")
			return

		case "ol":
			sb.WriteString("\n")
			idx := 1
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.Data == "li" {
					sb.WriteString(strings.Repeat("  ", listDepth))
					fmt.Fprintf(sb, "%d. ", idx)
					writeChildren(sb, c, readability, listDepth+1, false)
					sb.WriteString("\n")
					idx++
				}
			}
			sb.WriteString("\n")
			return

		case "blockquote":
			sb.WriteString("\n\n> ")
			writeChildren(sb, n, readability, listDepth, false)
			sb.WriteString("\n\n")
			return

		case "hr":
			sb.WriteString("\n\n---\n\n")
			return

		case "script", "style", "noscript":
			return
		}
	}

	// Default: recurse into children
	writeChildren(sb, n, readability, listDepth, inPre)
}

func writeChildren(
	sb *strings.Builder,
	n *html.Node,
	readability bool,
	listDepth int,
	inPre bool,
) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		convertRecursive(sb, c, readability, listDepth, inPre)
	}
}

// textContent returns the concatenated text content of a node.
func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return strings.TrimSpace(sb.String())
}

// getAttr returns the value of an attribute on an element node.
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// collapseWhitespace replaces runs of whitespace with a single space.
func collapseWhitespace(s string) string {
	var sb strings.Builder
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !inSpace {
				sb.WriteRune(' ')
				inSpace = true
			}
		} else {
			sb.WriteRune(r)
			inSpace = false
		}
	}
	return sb.String()
}

// cleanMarkdown normalizes excessive blank lines in the output.
func cleanMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankCount++
			if blankCount <= 2 {
				out = append(out, "")
			}
		} else {
			blankCount = 0
			out = append(out, line)
		}
	}

	result := strings.Join(out, "\n")
	result = strings.TrimSpace(result)
	if result != "" {
		result += "\n"
	}
	return result
}
