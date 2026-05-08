# foo

Pipe text through LLMs from the terminal. Pattern-based prompts,
streaming output, plugin ecosystem.

## Table of contents

- [Install](#install)
- [Quick start](#quick-start)
- [Usage](#usage)
  - [Patterns](#patterns)
  - [Strategies](#strategies)
  - [Fragments](#fragments)
  - [Schemas](#schemas)
  - [Tools](#tools)
  - [Embeddings](#embeddings)
  - [Models](#models)
- [Plugins](#plugins)
- [Configuration](#configuration)
- [Schema DSL reference](#schema-dsl-reference)
- [Privacy and data](#privacy-and-data)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Install

```sh
go install hop.top/foo@latest
```

Requires Go 1.26+. Set your provider key:

```sh
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

Verify:

```sh
foo "hello"
# Hello! How can I help you today?
```

## Quick start

```sh
# Summarize a file
cat report.md | foo -p summarize
# • Q3 revenue up 14% YoY driven by enterprise segment …

# Review a diff
git diff | foo "review this for bugs"
# Found 2 issues:
# 1. Nil pointer dereference on line 42 …

# Extract structured data
foo --schema "name, age int, role" "extract from: Jane, 32, engineer"
# {"name":"Jane","age":32,"role":"engineer"}

# Pipe a YouTube transcript through a pattern
foo-youtube "https://..." | foo -p extract_wisdom
```

## Usage

```sh
# One-shot prompt
foo "explain quicksort"

# Pipe stdin
cat essay.md | foo -p summarize

# Combine stdin + prompt
cat code.go | foo "find bugs in this"

# Pattern with strategy wrapping
foo --strategy cot -p analyze_paper "$(cat paper.txt)"

# Structured JSON output
foo --schema "name, age int, role" "extract from: Jane, 32, engineer"

# Streaming is default; block with --no-stream
foo --no-stream "long response expected"

# Inspect assembled prompt without calling LLM
foo --dry-run -p summarize "text"

# Interactive REPL (no args, interactive terminal)
foo
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--pattern` | `-p` | | Pattern (system prompt) to use |
| `--strategy` | `-s` | | Strategy to wrap system prompt |
| `--model` | `-m` | `claude-3-5-sonnet-latest` | Model override |
| `--no-stream` | | `false` | Wait for full response |
| `--dry-run` | | `false` | Print assembled prompt, skip LLM call |
| `--tool` | `-T` | | Enable specific tools by name |
| `--chain-limit` | | `5` | Max tool-call iterations |
| `--tools-debug` | | `false` | Log tool calls and results |
| `--tools-approve` | | `false` | Confirm before each tool execution |
| `--fragment` | `-f` | | Attach fragment(s) to user prompt |
| `--sf` | | | Attach fragment(s) to system prompt |
| `--schema` | | | Structured JSON output (schema name or DSL) |
| `--schema-multi` | | | Array JSON output (schema name or DSL) |

### Patterns

Reusable system prompts stored as `system.md` files:

```sh
foo pattern list
foo pattern add my-pattern "You are a helpful assistant that..."
foo pattern import ./prompts/review.md code-review
foo pattern remove old-pattern
foo -p my-pattern "input text"
```

Patterns are loaded from `$XDG_CONFIG_HOME/foo/patterns/` or
`.foo/patterns/` (project-local).

### Strategies

Meta-prompting wrappers that shape how the LLM reasons:

```sh
foo strategy list              # cot, tot, aot, reflexion, step-back
foo --strategy cot -p analyze "complex problem"
```

Custom strategies: add `.md` files to
`$XDG_CONFIG_HOME/foo/strategies/`. Format: prefix text, `---`
separator, suffix text.

### Fragments

Reusable text snippets attached to prompts:

```sh
foo fragment set persona "You are a senior Go engineer"
foo fragment set constraints "Keep answers under 200 words"
foo -f persona -f constraints "review this code"
foo --sf persona "review this code"   # attach to system prompt
```

### Schemas

Named JSON schemas for structured output:

```sh
foo schema set person "name, age int, email"
foo schema dsl "title, tags []str, score float"   # preview compiled schema
foo --schema person "extract: Jane Doe, 30, jane@x.com"
# {"name":"Jane Doe","age":30,"email":"jane@x.com"}

foo --schema-multi person "extract all people from this text"
# [{"name":"Jane Doe","age":30,"email":"jane@x.com"},{"name":...}]
```

See [Schema DSL reference](#schema-dsl-reference) for the full
type grammar.

### Tools

Function-calling with an agentic dispatch loop:

```sh
foo -T foo_time "what time is it?"
# The current time is 2026-04-12T14:32:00Z.

foo -T foo_time -T foo_version --tools-debug "system info"
foo -T foo_time --tools-approve "what time is it?"  # confirm each
```

### Embeddings

Vector search over local content:

```sh
foo embed "some important text" -c notes
foo embed-multi -c docs --file README.md
foo similar "how does auth work?" -c docs -n 5
foo collections list
foo collections delete old-notes
```

### Models

```sh
foo model list
foo model set-default claude-3-5-sonnet-latest
foo -m gpt-4o "use a specific model"
```

Supported providers:

| Provider | Scheme | API key env var | Status |
|----------|--------|-----------------|--------|
| Anthropic | `anthropic://` | `ANTHROPIC_API_KEY` | Stable |
| OpenAI | `openai://` | `OPENAI_API_KEY` | Stable |
| OpenAI-compatible | `openai://` | `OPENAI_API_KEY` | Via fallback (any unknown model prefix) |

Models with unrecognized prefixes fall back to the `openai://`
scheme, which works with OpenAI-compatible endpoints (OpenRouter,
Groq, Together, etc.) when `OPENAI_API_KEY` is set.

**Local models (Ollama, LM Studio, llama.cpp)** are not yet
supported — there is no `base_url` config to point at a local
endpoint. Tracked for a future release.

```sh
foo provider list
foo provider show anthropic
```

## Plugins

foo uses [kit/ext](https://hop.top/kit) for extensibility.
Plugins follow the Git convention: `foo <name>` dispatches to
a `foo-<name>` binary on `$PATH` (pending `hop-top/kit#0243`).

### Available plugins

| Plugin | Install | Purpose |
|--------|---------|---------|
| `foo youtube` | `go install hop.top/foo/cmd/foo-youtube@latest` | YouTube transcripts (requires `yt-dlp`) |
| `foo scrape` | `go install hop.top/foo/cmd/foo-scrape@latest` | URL to markdown |

Plugins output markdown to stdout, pipeable into foo:

```sh
foo youtube "https://youtube.com/watch?v=xxx" | foo -p summarize
foo scrape "https://example.com/article" | foo -p extract_wisdom

# With flags
foo youtube --timestamps --comments "https://youtube.com/watch?v=xxx"
```

All plugins support `--ext-info` for discovery metadata.

### Writing plugins

Any binary named `foo-*` in `$PATH` is automatically available
as `foo <name>`. Implement `--ext-info` to return JSON metadata:

```json
{"name":"myplugin","version":"0.1.0","description":"...","capabilities":["discover"]}
```

## Configuration

Layered: system defaults, user config, project override.

| Source | Path |
|--------|------|
| User | `$XDG_CONFIG_HOME/foo/config.yaml` |
| Project | `.foo.yaml` |

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `model` | string | `claude-3-5-sonnet-latest` | Default LLM model |
| `patterns_path` | string | `$XDG_CONFIG_HOME/foo/patterns` | Custom patterns directory |
| `accent` | string | `#E040FB` | TUI accent color (hex) |

### Environment variables

| Variable | Provider | Required |
|----------|----------|----------|
| `ANTHROPIC_API_KEY` | Anthropic (Claude) | For Claude models |
| `OPENAI_API_KEY` | OpenAI (GPT) | For GPT/o1 models |

## Schema DSL reference

The schema DSL compiles shorthand field definitions into JSON Schema.

**Format:** `field1, field2 type, field3 type, ...`

| Type | JSON Schema | Example |
|------|-------------|---------|
| `str` | `string` (default if omitted) | `name` or `name str` |
| `int` | `integer` | `age int` |
| `float` | `number` | `score float` |
| `bool` | `boolean` | `active bool` |
| `[]str` | `array` of strings | `tags []str` |
| `[]int` | `array` of integers | `scores []int` |

All fields are required. Default type is `str`.

```sh
# "name, age int, tags []str" compiles to:
{
  "type": "object",
  "properties": {
    "name":  {"type": "string"},
    "age":   {"type": "integer"},
    "tags":  {"type": "array", "items": {"type": "string"}}
  },
  "required": ["name", "age", "tags"]
}
```

## Privacy and data

foo sends your prompt text to third-party LLM APIs (Anthropic,
OpenAI, or other configured providers). No data is stored by foo
itself beyond local config and embeddings.

- **Prompts and responses** are sent to the provider's API. Review
  your provider's data retention policy.
- **Embeddings** are stored locally in
  `$XDG_DATA_HOME/foo/embeddings/`.
- **Session events** are recorded locally via WSM (workspace
  manager) for history and replay.
- **No telemetry** is collected by foo.

Use `--dry-run` to inspect the full prompt before it leaves your
machine.

## Troubleshooting

**"Error creating LLM client"** — API key not set or invalid.
Check `export ANTHROPIC_API_KEY=...` or `OPENAI_API_KEY=...`.

**"Error loading pattern"** — Pattern name not found. Run
`foo pattern list` to see available patterns.

**"REPL requires an interactive terminal"** — You ran `foo` with
no args and no TTY. Provide a prompt argument or pipe input.

**"schema not found and not valid DSL"** — The `--schema` value
is neither a saved schema name nor valid DSL syntax. Check the
[Schema DSL reference](#schema-dsl-reference).

**Empty or garbled streaming output** — Try `--no-stream` to
isolate. If non-streaming works, the issue is provider-specific
streaming support.

## Development

```sh
make build          # build foo binary
make test           # run all tests
go build ./...      # build everything (foo + plugins)
go vet ./...        # lint
```

### Project structure

```
main.go               CLI entrypoint
cmd/
  foo/commands/        command definitions (root, embed, schema, …)
  foo-youtube/         external plugin
  foo-scrape/          external plugin
internal/
  llm/                 provider-agnostic LLM client
  pattern/             pattern loading + management
  strategy/            meta-prompting strategy wrappers
  fragment/            reusable text snippets
  schema/              JSON schema DSL + storage
  tool/                function-calling framework
    builtin/           built-in tools (time, version)
  embed/               vector embeddings + similarity search
  workspace/           WSM adapter for session persistence
  ui/                  Bubble Tea REPL
  config/              layered configuration
```

## Contributing

Contributions welcome. Please open an issue before large changes.

1. Fork the repo
2. Create a feature branch
3. `go build ./...` and `go vet ./...` must pass
4. Open a PR against `main`

## License

MIT — see [LICENSE](LICENSE).
