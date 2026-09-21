# Use a local or self-hosted endpoint

Point foo at an OpenAI-compatible server you run yourself —
llama.cpp, vLLM, LM Studio, Ollama's OpenAI shim, or anything else
that speaks `/v1/chat/completions`.

## Use this when

- You run a model locally or on your own hardware.
- You reach a hosted model through a gateway or proxy that
  presents an OpenAI-compatible API.
- You want to keep prompts off a third-party provider.

For picking between *hosted* models, see
[route across models](route-across-models.md); for pinning one
hosted model id, see [configure models](configure-models.md).

## Point foo at the server

Set the endpoint under the `openai` scheme in
`~/.config/hop/llm.yaml`:

```yaml
providers:
  openai:
    base_url: http://127.0.0.1:8000/v1
```

Then call the model by the id your server advertises:

```sh
export OPENAI_API_KEY=any-non-empty-value
foo -m my-local-model "hello"
```

The key must be non-empty — foo prechecks it before dialing — but
servers that do not authenticate ignore its value.

`base_url` includes the `/v1` suffix (or whatever prefix your
server mounts); foo appends only `/chat/completions`.

### Which model ids work

Ask foo:

```sh
foo model list --endpoint http://127.0.0.1:11434/v1
```

```
PROVIDER         ID                   CONTEXT  TOOLS  REASONING  RELEASED  SOURCE
127.0.0.1:11434  llama3.2:3b          0        false  false                endpoint
127.0.0.1:11434  qwen2.5:7b-instruct  0        false  false                endpoint
```

Pass an `ID` verbatim to `-m`. Any id that does not match a known
hosted prefix (`gpt-`, `claude-`, `gemini-`, …) is treated as
OpenAI-compatible, which is what you want here.

`--endpoint` reads the server's `/v1/models`, which returns ids and
nothing else — so `CONTEXT` of `0` and `TOOLS`/`REASONING` of
`false` mean "not reported", not "zero" and "unsupported". The
`SOURCE` column marks where each row came from.

Once `base_url` is configured (or `LLM_BASE_URL` is exported), a
bare `foo model list` already lists that server instead of the
catalog; `--endpoint` is for pointing at one for a single
invocation. Precedence matches the completion path: `--endpoint`,
then the configured endpoint, then the catalog.

Because there is no metadata to filter on, every catalog filter is
rejected rather than silently ignored:

```console
$ foo model list --endpoint http://127.0.0.1:11434/v1 --reasoning
GENERIC: foo: --reasoning needs model metadata the catalog has and a live endpoint does not; drop --reasoning to filter the catalog, or drop --endpoint to list from the endpoint
```

### Listings are cached

Both sources are cached, on very different clocks:

| Source | TTL | Where |
|--------|-----|-------|
| Endpoint inventory (`/v1/models`) | 5 minutes | `$XDG_CACHE_HOME/foo/model-endpoint-cache.db` |
| aim catalog (models.dev) | 24 hours, with ETag revalidation | `$XDG_CACHE_HOME/hop/aim/` |

Five minutes for the endpoint because a local runtime's inventory
changes the moment you `ollama pull` or restart a server against a
different model directory. If a model you just pulled is missing
from the list, that is the cache — not foo failing to see it.

`--refresh` bypasses both and refetches:

```sh
foo model list --endpoint http://127.0.0.1:11434/v1 --refresh
```

A catalog listing prints its age as a stderr footer, so you can
tell a fresh list from a day-old one:

```
catalog: cached 1h ago
```

`FOO_CACHE_TTL` overrides the 5-minute endpoint window (a Go
duration string — `30s`, `1h`; `0` turns endpoint caching off
entirely), and `FOO_CACHE` relocates the cache directory. Neither
touches the catalog's 24h window, which aim owns.

One quirk worth knowing: a TTL is bound to an entry when it is
written, not when it is read. Lowering `FOO_CACHE_TTL` does not
shorten entries already on disk — `--refresh` is the way past one.

## Other ways to set the endpoint

Three levers, in increasing precedence:

| Lever | Scope | Use for |
|-------|-------|---------|
| `providers.openai.base_url` in `llm.yaml` | Every call | The endpoint you normally use |
| `LLM_BASE_URL` env var | One shell / one job | CI, or a temporary override |
| `?base_url=` on `--model` | One invocation | A one-off against a second server |

```sh
# Env override
LLM_BASE_URL=http://127.0.0.1:8000/v1 foo -m my-local-model "hello"

# Per-invocation, beats both of the above
foo -m "my-local-model?base_url=http://127.0.0.1:8000/v1" "hello"
```

`--model` also accepts a full URI when you want to name the scheme
explicitly:

```sh
foo -m "openai://my-local-model?base_url=http://127.0.0.1:8000/v1" "hello"
```

## If the server requires a token cap

Some servers reject requests that omit `max_tokens`. foo omits it by
default so providers apply their own limit; pass `--max-tokens` when
yours insists:

```sh
foo --max-tokens 512 -m my-local-model "hello"
```

A symptom worth recognizing: an error naming a context length far
larger than your prompt, such as

```
maximum context length is 8192 tokens, however your messages
resulted in at least 19 tokens
```

That arithmetic does not describe your prompt — it means the server
wanted an explicit cap. Set `--max-tokens` and retry.

## Troubleshooting

**Requests reach `api.openai.com` instead of your server.** The
`base_url` never resolved. Confirm the key is under `providers:` →
`openai:` (not at the top level), and that the file is the one foo
reads — `$XDG_CONFIG_HOME/hop/llm.yaml`, defaulting to
`~/.config/hop/llm.yaml`.

**`model "..." not available`.** kit maps the server's 404 onto this
message, so it usually means the path or the model id is wrong, not
that the model is missing. Re-run `foo model list --endpoint <url>
--refresh` for the exact id, and check whether `base_url` already
ends in `/v1` — a doubled `/v1/v1` also 404s.

**The server is only reachable from its own host.** Local inference
servers commonly bind to loopback. Forward the port rather than
rebinding it:

```sh
ssh -N -L 8000:localhost:8000 user@host
```

## Related docs

- [Configure models](configure-models.md) — find and pin a hosted model id
- [Route across models](route-across-models.md) — pool routing and `--budget`
- [Config reference](../reference/config.md) — every key and env var
- [Troubleshooting](../troubleshooting.md)
