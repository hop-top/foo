// Package strategy manages prompt strategy templates that wrap
// pattern system prompts with prefix/suffix instructions.
package strategy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hop.top/kit/go/core/xdg"
)

// Strategy represents a loaded strategy template.
type Strategy struct {
	Name        string
	Description string
	Prefix      string
	Suffix      string
}

// Manager handles strategy loading and listing.
type Manager struct {
	configDir string
}

// NewManager creates a strategy manager. It uses
// $XDG_CONFIG_HOME/foo/strategies/ for user strategies.
func NewManager() (*Manager, error) {
	confDir, err := xdg.ConfigDir("foo")
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	return &Manager{
		configDir: filepath.Join(confDir, "strategies"),
	}, nil
}

// List returns all available strategies (built-in + user).
func (m *Manager) List() []Strategy {
	seen := make(map[string]bool)
	var out []Strategy

	// User strategies override built-ins
	if entries, err := os.ReadDir(m.configDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				name := strings.TrimSuffix(e.Name(), ".md")
				s, err := m.loadFile(filepath.Join(m.configDir, e.Name()))
				if err != nil {
					continue
				}
				s.Name = name
				seen[name] = true
				out = append(out, *s)
			}
		}
	}

	// Built-in strategies
	for _, b := range builtinStrategies {
		if !seen[b.Name] {
			out = append(out, b)
		}
	}

	return out
}

// Load returns a strategy by name. User strategies take precedence
// over built-ins.
func (m *Manager) Load(name string) (*Strategy, error) {
	// Check user config dir first
	userFile := filepath.Join(m.configDir, name+".md")
	if s, err := m.loadFile(userFile); err == nil {
		s.Name = name
		return s, nil
	}

	// Check built-ins
	for _, b := range builtinStrategies {
		if b.Name == name {
			return &b, nil
		}
	}

	return nil, fmt.Errorf("strategy %q not found", name)
}

// WrapPrompt wraps a pattern system prompt with the strategy's
// prefix and suffix. Assembly order:
//
//	strategy_prefix + pattern_system_prompt + strategy_suffix
func (m *Manager) WrapPrompt(strategyName, sysPrompt string) (string, error) {
	s, err := m.Load(strategyName)
	if err != nil {
		return "", err
	}

	var parts []string
	if s.Prefix != "" {
		parts = append(parts, s.Prefix)
	}
	if sysPrompt != "" {
		parts = append(parts, sysPrompt)
	}
	if s.Suffix != "" {
		parts = append(parts, s.Suffix)
	}

	return strings.Join(parts, "\n\n"), nil
}

// EnsureDefaults writes built-in strategies to the config dir if
// they don't already exist.
func (m *Manager) EnsureDefaults() error {
	if err := os.MkdirAll(m.configDir, 0755); err != nil {
		return fmt.Errorf("create strategies dir: %w", err)
	}

	for _, b := range builtinStrategies {
		path := filepath.Join(m.configDir, b.Name+".md")
		if _, err := os.Stat(path); err == nil {
			continue // already exists
		}

		content := formatStrategy(b)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("write strategy %q: %w", b.Name, err)
		}
	}

	return nil
}

// loadFile parses a strategy .md file. The file format is:
//
//	prefix content
//	---
//	suffix content
//
// If no separator, the entire content is used as prefix.
func (m *Manager) loadFile(path string) (*Strategy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	s := &Strategy{}

	// Extract description from first line if it starts with "# "
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
		s.Description = strings.TrimPrefix(lines[0], "# ")
		if len(lines) > 1 {
			content = strings.TrimSpace(lines[1])
		} else {
			content = ""
		}
	}

	parts := strings.SplitN(content, "\n---\n", 2)
	s.Prefix = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		s.Suffix = strings.TrimSpace(parts[1])
	}

	return s, nil
}

// formatStrategy serializes a strategy to the .md file format.
func formatStrategy(s Strategy) string {
	var sb strings.Builder
	if s.Description != "" {
		fmt.Fprintf(&sb, "# %s\n\n", s.Description)
	}
	sb.WriteString(s.Prefix)
	if s.Suffix != "" {
		sb.WriteString("\n\n---\n\n")
		sb.WriteString(s.Suffix)
	}
	sb.WriteString("\n")
	return sb.String()
}
