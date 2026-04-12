# foo-embed — Vector Embedding Plugin

## Overview

Init()-registered plugin (CapRegistry) compiled into foo binary.
Generates vector embeddings via LLM providers; stores in SQLite
for content-addressed semantic search.

## Storage

SQLite table in workspace DB:

```sql
CREATE TABLE embeddings (
    id           TEXT PRIMARY KEY,     -- ULID
    collection   TEXT NOT NULL,        -- logical group
    content_hash TEXT NOT NULL,        -- SHA-256 of input
    vector       BLOB NOT NULL,        -- []float32 LE bytes
    metadata     TEXT DEFAULT '{}',    -- JSON
    created_at   TEXT NOT NULL,        -- RFC 3339
    UNIQUE(collection, content_hash)   -- content-addressed
);
CREATE INDEX idx_embeddings_collection
    ON embeddings(collection);
```

- Collections group related embeddings (e.g. "docs", "notes")
- Content-addressed: SHA-256(input) dedup; same content = skip
- Vector stored as raw little-endian float32 BLOB; dimension
  determined by model (1536 for text-embedding-3-small)

## CLI

```
foo embed "text"                        # embed text, default col
foo embed -c collection "text"          # into named collection
foo embed-multi -c docs --file notes.md # auto-chunk file
foo similar "query" -c docs -n 5        # top-N cosine sim
foo collections list                    # list collections
foo collections delete <name>           # drop collection
```

### `foo embed`

- Args: positional text or `--file` path
- Flags: `-c` collection (default: "default"),
  `--embed-model` override
- Computes SHA-256; skips if exists in collection
- Returns embedding ID + dimension

### `foo embed-multi`

- Requires `-c` and `--file`
- Chunks input (see Chunking below)
- Batch-embeds chunks; stores each with metadata:
  `{"source": "notes.md", "chunk": 3, "offset": 1024}`
- Reports: N chunks embedded, M skipped (dedup)

### `foo similar`

- Args: positional query text
- Flags: `-c` collection (required), `-n` top-N (default 5),
  `--threshold` min similarity (default 0.0)
- Embeds query; brute-force cosine sim over collection
- Output: ranked results with score, truncated content preview

### `foo collections`

- `list` — name, count, total size, created range
- `delete <name>` — drop all rows in collection; confirm prompt

## Embedding Interface

Located in `internal/embed/embedder.go`:

```go
// Embedder produces vector embeddings for text inputs.
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimension() int
}
```

Note: kit/llm/router defines single-text `Embedder` returning
`[]float64`. foo-embed uses batch `[]string` → `[][]float32`
for efficiency. Adapter bridges the two if kit adds embedding
support to Provider interface later.

### Default: OpenAI text-embedding-3-small

- 1536 dimensions, $0.02/1M tokens
- Configurable via `--embed-model` flag or config key
  `embed_model` in foo config
- Provider selection: same URI scheme logic as llm.NewClient

## Chunking

Located in `internal/embed/chunker.go`:

- Fixed-size: 512 tokens, 50-token overlap
- Markdown-aware: respect heading boundaries (# / ## / ###)
- Splits at paragraph breaks when possible; falls back to
  sentence then word boundaries
- Token counting: cl100k_base approximation (chars/4)

```go
type ChunkOptions struct {
    MaxTokens int  // default 512
    Overlap   int  // default 50
    Markdown  bool // default true
}

func Chunk(text string, opts ChunkOptions) []Chunk
```

## Plugin Registration

```go
// internal/embed/plugin.go
func init() {
    registry.Register(&embedPlugin{})
}

type embedPlugin struct{}

func (p *embedPlugin) Meta() ext.Metadata {
    return ext.Metadata{
        Name:    "embed",
        Version: "0.1.0",
        Description: "Vector embedding + semantic search",
    }
}

func (p *embedPlugin) Capabilities() ext.Capability {
    return ext.CapRegistry
}
```

Init creates/migrates the embeddings table. Close is no-op
(SQLite handle owned by workspace).

## Cosine Similarity

Pure Go in `internal/embed/similarity.go`:

```go
func CosineSimilarity(a, b []float32) float32
```

Brute-force scan over collection. Acceptable for <100k vectors.
Future: HNSW index if collections grow large.

## Dependencies

| Dep | Status | Notes |
|-----|--------|-------|
| kit/ext (CapRegistry) | available | plugin framework |
| kit/llm/router.Embedder | available | single-text iface |
| SQLite | available | already in foo deps |
| cosine similarity | new | pure Go, ~30 LOC |
| token counting | new | cl100k_base approx |

kit/llm does NOT have a top-level Embedder interface on Provider
or Client. Only `router.Embedder` exists (single-text, float64).
foo-embed must implement its own OpenAI embeddings API client
until kit/llm adds batch embedding support.

## Future / Stretch

- `--rag` flag on main `foo` command: auto-retrieve top-N from
  collection, inject as context before LLM prompt
- HNSW index for large collections (>10k vectors)
- Streaming embed progress bar for large files
- Multi-provider embedding (Anthropic, local models)
- Embed workspace events for session-aware semantic search
