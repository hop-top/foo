# foo

Pipe text through LLMs from the terminal. Pattern-based prompts,
streaming output, plugin ecosystem.

## Install

```sh
go install hop.top/foo@latest
```

Requires Go 1.26+. Set a provider key:

```sh
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

## First run

```sh
foo "hello"
# Hello! How can I help you today?
```

If that prints a model response, you are done. If it errors, read
[docs/troubleshooting.md](docs/troubleshooting.md).

## Documentation

| I want to... | Read |
|--------------|------|
| Understand what foo is | [docs/concepts.md](docs/concepts.md) |
| Try foo in 5 minutes | [docs/quickstart.md](docs/quickstart.md) |
| Embed content for semantic search | [docs/how-to/embed-content.md](docs/how-to/embed-content.md) |
| Search across embedded content | [docs/how-to/search-semantically.md](docs/how-to/search-semantically.md) |
| Manage reusable prompt fragments | [docs/how-to/manage-fragments.md](docs/how-to/manage-fragments.md) |
| Manage named system prompts (patterns) | [docs/how-to/manage-patterns.md](docs/how-to/manage-patterns.md) |
| Manage structured-output schemas | [docs/how-to/manage-schemas.md](docs/how-to/manage-schemas.md) |
| Apply chain-of-thought + other strategies | [docs/how-to/use-strategies.md](docs/how-to/use-strategies.md) |
| Switch or default a model | [docs/how-to/configure-models.md](docs/how-to/configure-models.md) |
| Run destructive commands in scripts | [docs/how-to/confirm-destructive-ops.md](docs/how-to/confirm-destructive-ops.md) |
| Upgrade foo in place | [docs/how-to/upgrade-foo.md](docs/how-to/upgrade-foo.md) |
| Install and use plugins (youtube, scrape, custom) | [docs/how-to/use-plugins.md](docs/how-to/use-plugins.md) |
| Write a new plugin (subcommand or LLM tool) | [docs/how-to/write-plugins.md](docs/how-to/write-plugins.md) |
| Look up the exact flag, command, or value | [docs/reference/commands.md](docs/reference/commands.md) |
| Look up a config key or env var | [docs/reference/config.md](docs/reference/config.md) |
| Look up the schema DSL grammar | [docs/reference/schema-dsl.md](docs/reference/schema-dsl.md) |
| Check compatibility + deprecations | [docs/reference/compatibility.md](docs/reference/compatibility.md) |
| Something broke | [docs/troubleshooting.md](docs/troubleshooting.md) |

## Plugins

foo discovers extension binaries on `$PATH`: any executable named
`foo-<name>` is callable as `foo <name>`. Tool-call extensions follow
the `foo-tool-<name>` convention.

Available first-party plugins:

| Plugin | Install | Purpose |
|--------|---------|---------|
| `foo youtube` | `go install hop.top/foo/cmd/foo-youtube@latest` | YouTube transcripts (needs `yt-dlp`) |
| `foo scrape` | `go install hop.top/foo/cmd/foo-scrape@latest` | URL to markdown |

Plugins emit markdown on stdout, pipeable into foo:

```sh
foo youtube "https://youtube.com/watch?v=xxx" | foo -p summarize
```

All plugins support `--ext-info` for discovery metadata. Full
walkthrough: [Use plugins](docs/how-to/use-plugins.md).

## License

MIT — see [LICENSE](LICENSE).
