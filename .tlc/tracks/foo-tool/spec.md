# foo-tool: Plugin-Based Tool Framework

> Framework (registry, dispatch) in core foo.
> Individual tools are plugins (built-in or external).

---

## 1. Core Tool Framework (foo-only)

### Tool Interface

```go
// internal/tool/tool.go
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage  // JSON Schema
    Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}
```

- `Parameters()` returns JSON Schema describing accepted input
- `Execute()` receives validated args, returns JSON result
- Tools are stateless; context carries cancellation + deadline

### Registry

```go
// internal/tool/registry.go
type Registry struct { /* ... */ }
func NewRegistry() *Registry
func (r *Registry) Register(t Tool) error      // duplicate = error
func (r *Registry) Get(name string) (Tool, bool)
func (r *Registry) List() []Tool               // stable order
func (r *Registry) ToolDefs() []llm.ToolDef     // for LLM request
```

- Built-in tools registered at init via `CapRegistry`
- External tools added after PATH scan via `CapDiscover`
- `ToolDefs()` converts registry contents to `kit/llm.ToolDef`
  slice ready for `CallWithTools`

### Dispatch Loop

Core agentic loop in `internal/tool/dispatch.go`:

```
1. User prompt arrives
2. Build llm.Request with messages
3. Call client.CallWithTools(ctx, req, registry.ToolDefs())
4. If resp.ToolCalls is empty → return resp.Content (done)
5. For each ToolCall:
   a. Lookup tool by name in registry
   b. If --tools-approve: prompt user for confirmation
   c. Execute tool with ToolCall.Arguments
   d. Append tool result as assistant/tool message
6. Increment chain counter
7. If chain counter >= --chain-limit → return with warning
8. Goto 3 (feed results back to LLM)
```

- Chain limit prevents runaway loops (default 5)
- Each iteration appends tool results as messages
- Final LLM response (no tool_calls) is returned to user

### CLI Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-T` | `[]string` | `[]` | Enable specific tools by name |
| `--chain-limit` | `int` | `5` | Max tool-call iterations |
| `--tools-debug` | `bool` | `false` | Log tool calls + results |
| `--tools-approve` | `bool` | `false` | Confirm before execute |

- `-T` empty = all registered tools available
- `-T foo_time -T foo_version` = only those two
- Flags live on root command; apply to both REPL + one-shot

---

## 2. Built-In Tools (CapRegistry)

Registered via `init()` in `internal/tool/builtin/`.

### foo_time

```json
{
  "name": "foo_time",
  "description": "Returns current date/time in RFC3339",
  "parameters": { "type": "object", "properties": {} }
}
```

Returns: `{"time": "2026-04-12T10:00:00Z"}`

### foo_version

```json
{
  "name": "foo_version",
  "description": "Returns foo binary version",
  "parameters": { "type": "object", "properties": {} }
}
```

Returns: `{"version": "0.1.0", "go": "1.23"}`

---

## 3. External Tools (CapDiscover)

### Discovery

Reuses `kit/ext/discover.Scanner` with prefix `foo-tool-`:

```
scanner := discover.Scanner{Prefix: "foo-tool-"}
found, _ := scanner.Scan()  // scans $PATH
```

Each found binary:
- Name derived by stripping prefix: `foo-tool-grep` -> `grep`
- Interrogated via `--ext-info` for metadata + parameters

### Extended --ext-info Response

Current `kit/ext/discover` returns name/version/description.
External tools MUST additionally return `parameters`:

```json
{
  "name": "grep",
  "version": "0.1.0",
  "description": "Search files with pattern",
  "parameters": {
    "type": "object",
    "properties": {
      "pattern": { "type": "string" },
      "path": { "type": "string" }
    },
    "required": ["pattern"]
  }
}
```

### Execution Protocol

JSON stdin/stdout protocol (no flags, no env):

**stdin** (tool receives):
```json
{
  "name": "grep",
  "arguments": { "pattern": "TODO", "path": "." }
}
```

**stdout** (tool returns):
```json
{
  "result": { "matches": ["file.go:10: // TODO fix"] },
  "error": null
}
```

- Exit 0 + `"error": null` = success
- Exit 0 + `"error": "msg"` = tool-level error (fed back to LLM)
- Exit non-zero = system error (logged, not fed to LLM)
- Timeout: 30s default, configurable per tool via ext-info

---

## 4. Hook Integration

Uses `kit/ext/hook.Bus` lifecycle hooks:

### BeforeRun (priority 10)

Fired before each LLM call in the dispatch loop.

Payload: `*ToolRequest{Tools []llm.ToolDef, Messages []llm.Message}`

Purpose:
- Inject available tools into LLM request
- Filter tools based on `-T` flag
- Log tool availability when `--tools-debug`

### AfterRun (priority 90)

Fired after each LLM response in the dispatch loop.

Payload: `*ToolResult{Calls []llm.ToolCall, Results []json.RawMessage}`

Purpose:
- Log tool usage (name, duration, success/fail)
- Record tool events to workspace via WSM adapter
- Emit metrics for observability

---

## 5. Security

### Opt-in Model

- Tools are NOT sent to LLM unless explicitly enabled
- `-T tool` flag or config enables specific tools
- No `-T` flag + no config = zero tools (safe default)

### Approval Gate

- `--tools-approve` requires user confirmation per tool call
- Prompt shows: tool name, arguments (pretty-printed)
- User responds y/n; timeout = reject
- REPL mode: inline prompt; one-shot mode: stderr prompt

### Sandboxing (future)

- External tools run in subprocess with restricted env
- No access to foo's API keys or config
- Timeout enforcement via context deadline

---

## 6. Dependencies

### Already exists in kit/llm (no changes needed)

| Type | Location | Purpose |
|------|----------|---------|
| `ToolDef` | `kit/llm.ToolDef` | Tool definition for LLM |
| `ToolCall` | `kit/llm.ToolCall` | Parsed tool invocation |
| `ToolResponse` | `kit/llm.ToolResponse` | LLM response w/ calls |
| `ToolCaller` | `kit/llm.ToolCaller` | Provider interface |
| `CallWithTools()` | `kit/llm.Client` | Dispatch w/ fallback |

kit/llm already has full tool-calling support:
- `ToolDef{Name, Description, Parameters}` matches our needs
- `CallWithTools(ctx, req, tools)` returns `ToolResponse`
- `ToolResponse.ToolCalls` contains parsed invocations
- Provider adapters (anthropic, openai) implement `ToolCaller`

### Already exists in kit/ext (no changes needed)

| Component | Location | Purpose |
|-----------|----------|---------|
| `discover.Scanner` | `kit/ext/discover` | PATH scan w/ prefix |
| `discover.Interrogate` | `kit/ext/discover` | `--ext-info` JSON |
| `hook.Bus` | `kit/ext/hook` | Lifecycle events |
| `registry.Registry` | `kit/ext/registry` | init() registration |
| `ext.Manager` | `kit/ext` | Multi-cap routing |

### Exists in kit/toolspec (useful, not required)

| Component | Purpose | Relation |
|-----------|---------|----------|
| `toolspec.ToolSpec` | CLI tool knowledge | Orthogonal; for CLI |
| `toolspec.Registry` | Spec resolution | Not LLM tool registry |

`kit/toolspec` describes CLI tools (commands, flags, errors).
foo's tool registry describes LLM-callable tools (name, params,
execute). Different domains; no overlap. Could cross-reference
later (e.g. toolspec error patterns inform tool descriptions).

### Needs changes in kit/ext/discover

`extInfoResponse` struct needs `Parameters json.RawMessage`
field to carry JSON Schema from `--ext-info`. Current struct:

```go
// Current — missing Parameters
type extInfoResponse struct {
    Name         string   `json:"name"`
    Version      string   `json:"version"`
    Description  string   `json:"description"`
    Capabilities []string `json:"capabilities"`
}
```

Required addition:

```go
// Add this field
Parameters json.RawMessage `json:"parameters,omitempty"`
```

This is backwards-compatible (omitempty). Existing plugins
that omit `parameters` continue to work unchanged.

### foo-only (new code)

| Package | Purpose |
|---------|---------|
| `internal/tool/tool.go` | `Tool` interface |
| `internal/tool/registry.go` | Tool registry |
| `internal/tool/dispatch.go` | Agentic dispatch loop |
| `internal/tool/external.go` | External tool adapter |
| `internal/tool/builtin/time.go` | `foo_time` |
| `internal/tool/builtin/version.go` | `foo_version` |
| `internal/llm/llm.go` | Add `CallWithTools` method |
| `cmd/foo/commands/root.go` | Add `-T`, `--chain-limit`, etc. |

### foo internal/llm changes

Current `Client` wraps `kit/llm.Client` but only exposes
`Prompt()` and `PromptStream()`. Needs new method:

```go
func (c *Client) CallWithTools(
    ctx context.Context,
    prompt string,
    tools []llm.ToolDef,
) (llm.ToolResponse, error)
```

Delegates to `c.client.CallWithTools()`. Requires the
underlying provider to implement `kit/llm.ToolCaller`
(anthropic + openai adapters already do).
