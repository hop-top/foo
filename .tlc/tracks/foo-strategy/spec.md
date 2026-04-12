# foo-strategy Plugin Spec

## Overview

Internal init()-registered plugin for strategy-based prompt wrapping.
Unlike foo-youtube and foo-scrape, this is NOT an external binary.
Uses `CapRegistry` — registered at init() time inside foo core.

Wraps any pattern's system prompt with a reasoning strategy template
(CoT, ToT, AoT, Reflexion, Self-Refine, etc.).

## Extension Contract

- Capability: `CapRegistry` (init-registered, not PATH-discovered)
- Implements `ext.Extension` interface:
  - `Meta()` returns name/version/description
  - `Capabilities()` returns `CapRegistry`
  - `Init(ctx)` loads strategy templates from config dir
  - `Close()` no-op

## Core Dependency

**Requires `--strategy` flag on foo root command.**

This flag must be added to `cmd/foo/commands/root.go`:

```
--strategy <name>   Apply reasoning strategy wrapper (cot, tot, etc.)
```

When set, the strategy plugin intercepts prompt assembly and wraps
the pattern's system prompt with the selected strategy template.

## Strategies

Built-in strategies (shipped as default templates):

| Name | Description |
|------|-------------|
| `cot` | Chain of Thought — step-by-step reasoning |
| `tot` | Tree of Thought — explore multiple paths |
| `aot` | Algorithm of Thought — algorithmic decomposition |
| `reflexion` | Self-evaluate and retry with reflection |
| `self-refine` | Iterative self-critique and improvement |
| `react` | Reason + Act interleaved loop |
| `step-back` | Abstract before solving |

## Strategy Templates

Stored as markdown files:

```
$XDG_CONFIG_HOME/foo/strategies/
  cot.md
  tot.md
  aot.md
  reflexion.md
  self-refine.md
  react.md
  step-back.md
```

### Template Format

Each `.md` file contains two sections separated by `---`:

```markdown
[prefix content — prepended to system prompt]
---
[suffix content — appended to system prompt]
```

If no `---` separator, entire template is treated as prefix.

### Custom Strategies

Users can add custom `.md` files to the strategies dir.
Any `.md` file in the directory is available by filename (sans ext).

## Usage

```sh
foo --strategy cot -p analyze_paper "text"
foo --strategy tot -p solve_problem "complex question"
foo --strategy reflexion -p code_review "code"
```

## Prompt Assembly Order

1. Load pattern system prompt (e.g. `analyze_paper`)
2. Load strategy template (e.g. `cot.md`)
3. Final system prompt = `prefix + pattern_prompt + suffix`
4. User content passed as user message (unchanged)

## Error Handling

- Unknown strategy name: exit 1 + error listing available strategies
- Missing strategies dir: create with defaults on first use
- Malformed template: warn on stderr, use pattern prompt unmodified

## Registration

```go
func init() {
    ext.Register(&StrategyPlugin{})
}
```

Plugin registered in foo's main package import chain.
No PATH discovery needed.
