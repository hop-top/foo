package fragment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Resolve retrieves fragment content by alias. This is the primary entry
// point for prompt composition.
func (m *Manager) Resolve(ctx context.Context, alias string) ([]byte, error) {
	return m.Content(ctx, alias)
}

// ResolveMultiple resolves a list of aliases and joins their content with
// the standard separator.
func (m *Manager) ResolveMultiple(ctx context.Context, aliases []string) (string, error) {
	var parts []string
	for _, alias := range aliases {
		content, err := m.Resolve(ctx, alias)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", alias, err)
		}
		parts = append(parts, string(content))
	}
	return strings.Join(parts, "\n---\n"), nil
}

// SetFromSource resolves a source string to content and stores it under
// the given alias. Source can be:
//   - URL (http:// or https://)
//   - File path (anything else)
//
// Use Set directly for stdin content.
func (m *Manager) SetFromSource(ctx context.Context, alias, source string) error {
	var content []byte
	var sourceType, sourceRef string

	switch {
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		sourceType = "url"
		sourceRef = source
		data, err := fetchURL(ctx, source)
		if err != nil {
			return fmt.Errorf("fetch URL: %w", err)
		}
		content = data

	default:
		sourceType = "file"
		sourceRef = source
		data, err := os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		content = data
	}

	if err := m.Set(ctx, alias, sourceType, sourceRef, content); err != nil {
		return err
	}

	return m.updateIndex(ctx, alias, true)
}

// SetFromReader stores content read from r under the given alias.
func (m *Manager) SetFromReader(ctx context.Context, alias string, r io.Reader) error {
	content, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	if err := m.Set(ctx, alias, "stdin", "", content); err != nil {
		return err
	}

	return m.updateIndex(ctx, alias, true)
}

// RemoveWithIndex removes a fragment alias and updates the index.
func (m *Manager) RemoveWithIndex(ctx context.Context, alias string) error {
	if err := m.Remove(ctx, alias); err != nil {
		return err
	}
	return m.updateIndex(ctx, alias, false)
}

// fetchURL fetches content from a URL.
func fetchURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	return io.ReadAll(resp.Body)
}
