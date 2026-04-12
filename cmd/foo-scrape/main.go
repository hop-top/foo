// foo-scrape is a standalone binary that converts URLs to markdown.
// It is an external plugin for foo and imports zero foo internal packages.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/net/html"
)

const version = "0.1.0"

func main() {
	var (
		readability = true
		raw         = false
		extInfo     = false
		url         string
	)

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--readability":
			readability = true
			raw = false
		case "--raw":
			raw = true
			readability = false
		case "--ext-info":
			extInfo = true
		case "--help", "-h":
			printUsage()
			return
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "unknown flag: %s\n", args[i])
				os.Exit(1)
			}
			url = args[i]
		}
	}

	if extInfo {
		info := map[string]any{
			"name":         "scrape",
			"version":      version,
			"description":  "URL to markdown conversion with readability",
			"capabilities": []string{"discover"},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "")
		_ = enc.Encode(info)
		return
	}

	if url == "" {
		fmt.Fprintln(os.Stderr, "error: URL argument required")
		printUsage()
		os.Exit(1)
	}

	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching URL: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing HTML: %v\n", err)
		os.Exit(1)
	}

	if raw {
		md := convertNode(doc, false)
		fmt.Print(cleanMarkdown(md))
	} else if readability {
		title := extractTitle(doc)
		content := extractArticleContent(doc)
		if title != "" {
			fmt.Printf("# %s\n\n", title)
		}
		md := convertNode(content, true)
		fmt.Print(cleanMarkdown(md))
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: foo-scrape [flags] <url>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Flags:")
	fmt.Fprintln(os.Stderr, "  --readability  Extract main content (default)")
	fmt.Fprintln(os.Stderr, "  --raw          Convert full HTML to markdown")
	fmt.Fprintln(os.Stderr, "  --ext-info     Print plugin info as JSON")
	fmt.Fprintln(os.Stderr, "  -h, --help     Show help")
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

// Ensure http.Get is used (suppress unused import lint).
var _ = io.Discard
