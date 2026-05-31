package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var version = "dev"

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

func main() {
	transcript := flag.Bool("transcript", true, "Extract transcript")
	timestamps := flag.Bool("timestamps", false, "Include timestamps in transcript")
	comments := flag.Bool("comments", false, "Include top comments")
	metadata := flag.Bool("metadata", true, "Include video metadata")
	showExtInfo := flag.Bool("ext-info", false, "Print extension info as JSON")

	flag.Parse()

	if *showExtInfo {
		info := extInfo{
			Name:         "youtube",
			Version:      version,
			Description:  "YouTube transcript and metadata extraction",
			Capabilities: []string{"discover"},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(os.Stderr, "error encoding ext-info: %v\n", err)
			os.Exit(1)
		}
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: YouTube URL required")
		fmt.Fprintln(os.Stderr, "usage: foo-youtube [flags] <url>")
		os.Exit(1)
	}

	url := args[0]
	if !isYouTubeURL(url) {
		fmt.Fprintf(os.Stderr, "error: invalid YouTube URL: %s\n", url)
		os.Exit(1)
	}

	if err := checkYTDLP(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var md *videoMetadata
	if *metadata {
		var err error
		md, err = fetchMetadata(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error fetching metadata: %v\n", err)
			os.Exit(1)
		}
	}

	var transcriptText string
	if *transcript {
		var err error
		transcriptText, err = fetchTranscript(url, *timestamps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error fetching transcript: %v\n", err)
			os.Exit(1)
		}
	}

	var commentList []comment
	if *comments {
		var err error
		commentList, err = fetchComments(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not fetch comments: %v\n", err)
			// Non-fatal: continue without comments
		}
	}

	renderMarkdown(os.Stdout, md, transcriptText, commentList)
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

func fetchMetadata(url string) (*videoMetadata, error) {
	cmd := exec.Command("yt-dlp",
		"--dump-json",
		"--no-download",
		"--no-playlist",
		url,
	)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp metadata: %w", err)
	}

	var md videoMetadata
	if err := json.Unmarshal(out, &md); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	return &md, nil
}

func fetchTranscript(url string, withTimestamps bool) (string, error) {
	args := []string{
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-lang", "en",
		"--sub-format", "json3",
		"--no-playlist",
		"-o", "-",
		url,
	}

	cmd := exec.Command("yt-dlp", args...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		// Fallback: try getting subtitles via different approach
		return fetchTranscriptFallback(url, withTimestamps)
	}

	return parseTranscript(out, withTimestamps)
}

func fetchTranscriptFallback(url string, withTimestamps bool) (string, error) {
	// Use yt-dlp to get subtitle file
	args := []string{
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-lang", "en",
		"--sub-format", "vtt",
		"--print", "subtitle",
		"--no-playlist",
		url,
	}

	cmd := exec.Command("yt-dlp", args...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
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
	TStartMs int         `json:"tStartMs"`
	Segs     []json3Seg  `json:"segs"`
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

func fetchComments(url string) ([]comment, error) {
	cmd := exec.Command("yt-dlp",
		"--dump-json",
		"--no-download",
		"--write-comments",
		"--no-playlist",
		"--extractor-args", "youtube:max_comments=20",
		url,
	)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
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
