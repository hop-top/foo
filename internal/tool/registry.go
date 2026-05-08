package tool

import (
	"fmt"
	"sort"
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

// Filter returns a new registry containing only the named tools.
// Missing names are silently skipped.
func (r *Registry) Filter(names []string) *Registry {
	filtered := NewRegistry()
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, n := range names {
		if t, ok := r.tools[n]; ok {
			filtered.tools[n] = t
			filtered.order = append(filtered.order, n)
		}
	}
	return filtered
}
