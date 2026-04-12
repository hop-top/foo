# foo

Pipe text through LLMs from the terminal. Pattern-based prompts,
streaming output, plugin ecosystem.

## Why

You have text. You want an LLM to process it. You don't want to
open a browser, paste into a chat window, wait, copy the result.

foo lets you stay in the terminal:

```sh
cat report.md | foo -p summarize
git diff | foo "review this for bugs"
foo-youtube "https://..." | foo -p extract_wisdom
curl -s api/data | foo --schema "name, score int" "extract people"
```

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

### Patterns

Reusable system prompts stored as `system.md` files:

```sh
foo pattern list
foo pattern add my-pattern "You are a helpful assistant that..."
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
foo schema dsl "title, tags []str, score float"   # preview
foo --schema person "extract: Jane Doe, 30, jane@x.com"
foo --schema-multi person "extract all people from this text"
```

### Tools

Function-calling with an agentic dispatch loop:

```sh
foo -T foo_time "what time is it?"
foo -T foo_time -T foo_version --tools-debug "system info"
foo -T foo_time --tools-approve "what time is it?"  # confirm each
```

Flags: `-T <name>`, `--chain-limit N` (default 5),
`--tools-debug`, `--tools-approve`.

### Embeddings

Vector search over local content:

```sh
foo embed "some important text" -c notes
foo embed-multi -c docs --file README.md
foo similar "how does auth work?" -c docs -n 5
foo collections list
```

### Models

```sh
foo model list
foo model set-default claude-3-5-sonnet-latest
foo -m gpt-4o "use a specific model"
```

## Plugins

foo uses [kit/ext](https://hop.top/kit) for extensibility.

### External plugins

Standalone binaries discovered via `$PATH`:

| Plugin | Install | Purpose |
|--------|---------|---------|
| `foo-youtube` | `go install hop.top/foo/cmd/foo-youtube@latest` | YouTube transcripts |
| `foo-scrape` | `go install hop.top/foo/cmd/foo-scrape@latest` | URL to markdown |

External plugins output markdown to stdout, pipeable into foo:

```sh
foo-youtube --transcript "https://youtube.com/watch?v=xxx" | foo -p summarize
foo-scrape "https://example.com/article" | foo -p extract_wisdom
```

All external plugins support `--ext-info` for discovery metadata.

### Writing plugins

Any binary named `foo-*` in `$PATH` is discoverable. Implement
`--ext-info` to return JSON metadata:

```json
{"name":"myplugin","version":"0.1.0","description":"...","capabilities":["discover"]}
```

## Configuration

Layered: system defaults, user config, project override.

| Source | Path |
|--------|------|
| User | `$XDG_CONFIG_HOME/foo/config.yaml` |
| Project | `.foo.yaml` |

```yaml
model: claude-3-5-sonnet-latest
patterns_path: ~/my-patterns
accent: "#E040FB"
```

## Project structure

```
cmd/
  foo/              main CLI binary
  foo-youtube/      external plugin
  foo-scrape/       external plugin
internal/
  llm/              provider-agnostic LLM client
  pattern/          pattern loading + management
  strategy/         meta-prompting strategy wrappers
  fragment/         reusable text snippets
  schema/           JSON schema DSL + storage
  tool/             function-calling framework
    builtin/        built-in tools (time, version)
  embed/            vector embeddings + similarity search
  workspace/        WSM adapter for session persistence
  ui/               Bubble Tea REPL
  config/           layered configuration
```

## Development

```sh
make build          # build foo binary
make test           # run all tests
go build ./...      # build everything (foo + plugins)
go vet ./...        # lint
```

## Contributing

Contributions welcome. Please open an issue before large changes.

1. Fork the repo
2. Create a feature branch
3. `go build ./...` and `go vet ./...` must pass
4. Open a PR against `main`

## License

MIT License. See [LICENSE](LICENSE) for details.

Copyright (c) Idea Crafters LLC
