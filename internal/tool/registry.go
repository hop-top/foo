package tool

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"hop.top/kit/go/ai/llm"
)

// Registry holds named tools and converts them to LLM-ready definitions.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string // insertion order for stable List()
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. Returns an error if a tool
// with the same name is already registered.
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool: duplicate registration %q", name)
	}
	r.tools[name] = t
	r.order = append(r.order, name)
	return nil
}

// Get returns the tool with the given name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools in stable (sorted) order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, len(r.order))
	copy(names, r.order)
	sort.Strings(names)

	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// ToolDefs converts the registry contents to kit/llm.ToolDef slice
// ready for CallWithTools.
func (r *Registry) ToolDefs() []llm.ToolDef {
	tools := r.List()
	defs := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		defs[i] = llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		}
	}
	return defs
}

// Select returns a new registry containing only the named tools.
// Repeated names are kept once. Any name not in the registry fails the
// whole selection with an *UnknownToolError: running the model with a
// silently shortened tool list turns a typo into a wrong answer.
func (r *Registry) Select(names []string) (*Registry, error) {
	selected := NewRegistry()
	r.mu.RLock()
	defer r.mu.RUnlock()

	var unknown []string
	seenUnknown := make(map[string]bool)
	for _, n := range names {
		t, ok := r.tools[n]
		if !ok {
			if !seenUnknown[n] {
				seenUnknown[n] = true
				unknown = append(unknown, n)
			}
			continue
		}
		if _, dup := selected.tools[n]; dup {
			continue
		}
		selected.tools[n] = t
		selected.order = append(selected.order, n)
	}
	if len(unknown) > 0 {
		available := make([]string, len(r.order))
		copy(available, r.order)
		sort.Strings(available)
		return nil, &UnknownToolError{Unknown: unknown, Available: available}
	}
	return selected, nil
}

// UnknownToolError reports tool names that matched nothing in the
// registry, alongside every name that would have.
type UnknownToolError struct {
	// Unknown lists the unmatched names in the order given.
	Unknown []string
	// Available lists every registered tool name, sorted.
	Available []string
}

// Summary names the unmatched tools without the available list, e.g.
// `unknown tool "x"` or `unknown tools "a", "b"`.
func (e *UnknownToolError) Summary() string {
	quoted := make([]string, len(e.Unknown))
	for i, n := range e.Unknown {
		quoted[i] = strconv.Quote(n)
	}
	noun := "tool"
	if len(e.Unknown) > 1 {
		noun = "tools"
	}
	return "unknown " + noun + " " + strings.Join(quoted, ", ")
}

func (e *UnknownToolError) Error() string {
	return fmt.Sprintf("%s; available: %s", e.Summary(), strings.Join(e.Available, ", "))
}

// SourceBuiltin is the SourceOf value for tools compiled into foo.
const SourceBuiltin = "builtin"

// SourceOf reports where a tool comes from: what the tool says when it
// knows (a spec tool: "builtin", "user:<path>", …), the binary path for
// a tool backed by an external executable, SourceBuiltin otherwise.
func SourceOf(t Tool) string {
	if s, ok := t.(interface{ Source() string }); ok {
		return s.Source()
	}
	if p, ok := t.(interface{ Path() string }); ok {
		return p.Path()
	}
	return SourceBuiltin
}
