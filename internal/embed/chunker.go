package embed

import "strings"

const (
	defaultChunkSize    = 512 // tokens (approx chars/4)
	defaultChunkOverlap = 50  // tokens
	charsPerToken       = 4
)

// ChunkOptions configures text chunking.
type ChunkOptions struct {
	ChunkSize int // in approximate tokens
	Overlap   int // in approximate tokens
}

// Chunk splits text into overlapping chunks, preferring markdown heading
// boundaries when possible.
func Chunk(text string, opts *ChunkOptions) []string {
	size := defaultChunkSize
	overlap := defaultChunkOverlap
	if opts != nil {
		if opts.ChunkSize > 0 {
			size = opts.ChunkSize
		}
		if opts.Overlap > 0 {
			overlap = opts.Overlap
		}
	}

	charSize := size * charsPerToken
	charOverlap := overlap * charsPerToken

	// Try markdown-aware splitting first.
	sections := splitMarkdown(text)
	if len(sections) > 1 {
		return chunkSections(sections, charSize, charOverlap)
	}

	// Fall back to fixed-size chunking.
	return fixedChunk(text, charSize, charOverlap)
}

// splitMarkdown splits on markdown headings (lines starting with #).
func splitMarkdown(text string) []string {
	lines := strings.Split(text, "\n")
	var sections []string
	var cur strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") && cur.Len() > 0 {
			sections = append(sections, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		sections = append(sections, strings.TrimSpace(cur.String()))
	}
	return sections
}

// chunkSections splits each markdown section into fixed-size chunks if
// the section exceeds charSize, then merges small sections together.
func chunkSections(sections []string, charSize, charOverlap int) []string {
	var chunks []string
	var buf strings.Builder

	for _, sec := range sections {
		// If adding this section exceeds limit, flush buffer.
		if buf.Len() > 0 && buf.Len()+len(sec) > charSize {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
			// Overlap: keep tail of buffer.
			tail := buf.String()
			buf.Reset()
			if len(tail) > charOverlap {
				buf.WriteString(tail[len(tail)-charOverlap:])
			}
		}

		if len(sec) > charSize {
			// Flush any buffer first.
			if buf.Len() > 0 {
				chunks = append(chunks, strings.TrimSpace(buf.String()))
				buf.Reset()
			}
			chunks = append(chunks, fixedChunk(sec, charSize, charOverlap)...)
		} else {
			if buf.Len() > 0 {
				buf.WriteString("\n\n")
			}
			buf.WriteString(sec)
		}
	}

	if buf.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(buf.String()))
	}
	return chunks
}

func fixedChunk(text string, charSize, charOverlap int) []string {
	if charSize <= 0 {
		return []string{text}
	}
	if charOverlap < 0 {
		charOverlap = 0
	}
	if charOverlap >= charSize {
		charOverlap = charSize - 1
	}
	if len(text) <= charSize {
		return []string{text}
	}

	step := charSize - charOverlap
	var chunks []string
	for start := 0; start < len(text); start += step {
		end := start + charSize
		if end > len(text) {
			end = len(text)
		}
		chunks = append(chunks, text[start:end])
		if end == len(text) {
			break
		}
	}
	return chunks
}
