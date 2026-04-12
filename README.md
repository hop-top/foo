# foo — Opinionated LLM CLI/REPL

> [!WARNING]
> **🚧 Do Not Use — History Will Be Rewritten 🚧**
>
> This repo is undergoing major restructuring as we selectively
> open-source internal tools built at
> [Idea Crafters LLC](https://ideacrafters.com). Git history **will be
> force-pushed and rewritten** multiple times. Do not fork, clone, or
> depend on this repo in any capacity until we tag a stable release.

foo (hop.top/foo) is a fully customizable CLI and REPL for interacting with LLMs, 
powered by the `hop.top/kit` ecosystem.

## Features

- **Session Persistence**: Built on top of `wsm`, every interaction is logged 
  to an event-sourced workspace. Never lose your history.
- **Provider Agnostic**: Powered by `kit/llm`, supporting Anthropic, OpenAI, 
  and more via standard URIs.
- **Layered Configuration**: Uses `kit/config` for system, user, and project-level settings.
- **Standard CLI Contract**: Built with `kit/cli`, providing consistent 
  behavior, themed output, and structured logging with `kit/log`.
- **Self-Upgrading**: Integrated `kit/upgrade` for easy maintenance.
- **Pattern System**: Opinionated system prompts (Patterns) for common tasks.
- **Interactive REPL**: A full TUI REPL built with `bubbletea`.

## Usage

### Single Shot Prompt

```bash
./foo "Hello, how are you?"
```

### Using a Pattern

```bash
./foo -p summarize "Long text to summarize..."
```

### REPL Mode

```bash
./foo
```

### List Patterns

```bash
./foo list-patterns
```

### Upgrade

```bash
./foo upgrade
```

## Configuration

Configuration is loaded from:
- User: `~/.config/foo/config.yaml`
- Project: `.foo.yaml`

Example `config.yaml`:
```yaml
model: "gpt-4o"
patterns_path: "~/my-patterns"
accent: "#FF5722"
```

## Development

Built with Go 1.24+ and the `hop.top/kit` library.
