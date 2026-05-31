# Config reference

Configuration sources, precedence, keys, and environment-variable
equivalents.

## Sources and precedence

foo loads configuration in layers; later layers override earlier
ones.

| Layer | Path | Order |
|-------|------|-------|
| Built-in defaults | (compiled) | Lowest |
| System | `/etc/foo/config.yaml` | |
| User | `$XDG_CONFIG_HOME/foo/config.yaml` (default `~/.config/foo/config.yaml`) | |
| Project | `.foo.yaml` (in the current directory) | |
| Extra `-c` files | Per `-c <path>` invocation | |
| Environment | `FOO_*` variables | |
| `-c key=value` | Per `-c key=value` invocation | Highest |

`-c` is supplied by kit, not foo. Its semantics:

- `-c <path>` loads an additional config file after the
  discovered ones.
- `-c key=value` overrides a single value (dotted keys for
  nesting; the value is parsed as YAML, then falls back to a
  literal string).
- `-c` flags win over any file layer.

## Configuration keys

| Key | Type | Default | Description | Env equivalent |
|-----|------|---------|-------------|----------------|
| `model` | string | `claude-3-5-sonnet-latest` | Default LLM model id | `FOO_MODEL` |
| `patterns_path` | string | `$XDG_CONFIG_HOME/foo/patterns` | Directory for pattern files | `FOO_PATTERNS_PATH` |
| `accent` | string | `#E040FB` | TUI accent color (hex) | `FOO_ACCENT` |
| `secrets.backend` | string | `env` | Secret store backend id | `FOO_SECRETS_BACKEND` |
| `secrets.prefix` | string | `""` | Prefix applied to secret key lookups | `FOO_SECRETS_PREFIX` |
| `secrets.service` | string | `""` | Service identifier for the backend | `FOO_SECRETS_SERVICE` |

### Example `config.yaml`

```yaml
model: claude-3-5-sonnet-latest
patterns_path: /home/me/.config/foo/patterns
accent: "#E040FB"

secrets:
  backend: env
  prefix: ""
  service: ""
```

## Environment variables

### Provider keys

| Variable | Used by | Required for |
|----------|---------|--------------|
| `ANTHROPIC_API_KEY` | Anthropic provider | Claude models |
| `OPENAI_API_KEY` | OpenAI provider + OpenAI-compatible fallback | GPT/o-series models, all embeddings, OpenAI-compatible endpoints |
| `GOOGLE_API_KEY` | Google/Gemini provider | Gemini models (when scheme registered) |

`foo provider show <scheme>` reports each scheme's expected key
and whether foo can see it.

### foo-specific env vars

| Variable | Overrides | Notes |
|----------|-----------|-------|
| `FOO_MODEL` | `model` | Default model id |
| `FOO_PATTERNS_PATH` | `patterns_path` | Directory for patterns |
| `FOO_ACCENT` | `accent` | TUI accent color |
| `FOO_SECRETS_BACKEND` | `secrets.backend` | Secret backend |
| `FOO_SECRETS_PREFIX` | `secrets.prefix` | Secret key prefix |
| `FOO_SECRETS_SERVICE` | `secrets.service` | Secret service id |

### XDG variables (kit-shared)

foo follows XDG base directories. The defaults below are what
applies when the variable is unset.

| Variable | Default | Holds |
|----------|---------|-------|
| `XDG_CONFIG_HOME` | `~/.config` | `foo/config.yaml`, `foo/patterns/` |
| `XDG_STATE_HOME` | `~/.local/state` | `foo/embeddings.db`, `foo/schemas.db`, upgrade state |
| `XDG_DATA_HOME` | `~/.local/share` | WSM workspace store (fragments) |

## Kit `-c/--config` interaction

`-c` and `-c key=value` are applied last, after env. This is
intentional: command-line overrides always win, so a CI job that
sets `-c model=gpt-4o-mini` does not silently lose to a
project-local `.foo.yaml`.

Examples:

```sh
# Layer an extra config file on top of the discovered ones
foo -c ./ci.foo.yaml "some prompt"

# Single-value override (dotted key for nesting)
foo -c secrets.backend=keyring "some prompt"

# YAML-parsed value
foo -c "secrets={backend: keyring, service: foo}" "some prompt"
```

## Related docs

- [Configure models](../how-to/configure-models.md) — set/override the model.
- [Concepts: where state lives](../concepts.md#where-state-lives) — directory layout.
- [Compatibility](compatibility.md) — kit version requirements for `-c`.
