# Contributing

Thanks for your interest in `foo`. This file covers contribution norms;
see [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) for community expectations.

## Conventional Commits

All commits and PR titles follow
[Conventional Commits v1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>(<scope>)!?: <subject>
```

Allowed types: `feat`, `fix`, `perf`, `refactor`, `chore`, `docs`, `style`,
`test`, `ci`, `build`.

`feat`, `fix`, `perf` are user-facing (bump versions, appear in changelog).
The rest are hidden. CI/workflow changes use `ci:` — never `fix(ci):`.

Breaking changes use `!` after the type/scope or a `BREAKING CHANGE:` trailer.

## PR gate

Before opening a PR, confirm locally:

```
go build ./...
go vet ./...
go test ./...
```

CI runs the same gate plus `actionlint` on any workflow change.

## Recorded model calls

Tests that talk to a model provider replay [xrr](https://github.com/hop-top/xrr)
cassettes recorded from the real provider; `go test ./...` never dials one.
Recording happens only when you ask for it with `-update`. Cassettes never
hold keys: auth headers are not recorded, Gemini's `?key=` is dropped from
the URL, and every run fails if a cassette matches a key shape or the key
you recorded with. A plain `go test ./internal/llmxrr` also scans every
committed `testdata/cassettes` dir for key shapes and stored failures.

| Suite | Cassettes | Record |
|-------|-----------|--------|
| Tool round, OpenAI | `internal/tool/testdata/cassettes/openai` | `OPENAI_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/openai' -update` |
| Tool round, Ollama | `internal/tool/testdata/cassettes/ollama` | `FOO_RECORD_OLLAMA_BASE_URL=http://<ollama-host>:11434/v1 go test ./internal/tool -run 'TestRecordedToolRound/ollama' -update` (model `ornith:9b`, any OpenAI-compatible Ollama `/v1`) |
| Tool round, OpenRouter | `internal/tool/testdata/cassettes/openrouter` | `OPENROUTER_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/openrouter' -update` (model `openai/gpt-4.1-nano`) |
| Tool round, Anthropic | `internal/tool/testdata/cassettes/anthropic` | `ANTHROPIC_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/anthropic' -update` |
| Tool round, Gemini | `internal/tool/testdata/cassettes/gemini` | `GOOGLE_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/gemini' -update` (`GEMINI_API_KEY` also works) |
| Endpoint catalog | `internal/llm/testdata/cassettes` | `go test ./internal/llm -run 'TestEndpointCatalog_RealResponseFromCassette' -update` (an OpenAI-compatible server at `127.0.0.1:11434/v1`) |
| e2e `-T` shim calls | `tests/e2e/testdata/cassettes/tool-model` | `OPENAI_API_KEY=... go test ./tests/e2e -run 'TestToolShims\|TestToolPlugins' -update -timeout 60m` |

A provider with no cassettes yet is skipped, and the skip names its record
command. Before re-recording a tool round, delete that provider's cassette
directory, so no stale recording is left behind.

The e2e suite builds foo with `-tags xrr`, which compiles in `xrr_seam.go`:
with `XRR_MODE` set, the binary sends model calls through the cassette. Under
`-update` it records only the calls that have no recording yet, so adding a
case records just that case; delete a cassette file, or the whole directory,
to record it again. Paths under the test's scratch dir are stored as
`{{root}}`, and tool results are left out of the fingerprint, since what a
local command prints differs between macOS and Linux.

Only successful exchanges are recorded. A transport error (a bad base URL,
a refused connection) or a non-2xx status (a 400 for a bad request, a 401
for a wrong key, a 429 or 5xx) fails the recording with `refusing to record
a failed exchange`, naming the method, path and status, and writes nothing;
fix the cause and record again. Once a request is refused, every later
unrecorded request in that run fails the same way without reaching the
provider, so SDK retries and fallbacks spend no calls. A cassette that
already holds a failure (recorded before this rule) never replays as a
reply: replay fails with `cassette holds a failed exchange`, and recording
again treats the entry as missing and overwrites it.

A replay that "matches no recording" means foo now sends a different
request than the one recorded. If the change is intended, record again; if
not, the test caught it. When the error leads with a refusal or a stored
failure, that is the cause: the miss is the retry or fallback after it.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please)
on merge to `main`. Merging the standing release PR tags `v<version>` and
triggers GoReleaser to publish binaries.
