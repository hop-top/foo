package fragment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"hop.top/foo/internal/workspace"
)

// AliasEntry is the JSON stored in StateStore under "fragment:alias:<name>".
type AliasEntry struct {
	ArtifactID string `json:"artifact_id"`
	Source      string `json:"source"`
	SourceRef   string `json:"source_ref,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// Manager handles fragment storage and retrieval via workspace stores.
type Manager struct {
	store       workspace.Store
	workspaceID string
	sessionID   string
}

// NewManager creates a fragment manager backed by the given workspace store.
func NewManager(store workspace.Store, workspaceID, sessionID string) *Manager {
	return &Manager{
		store:       store,
		workspaceID: workspaceID,
		sessionID:   sessionID,
	}
}

// aliasKey returns the StateStore key for a fragment alias.
func aliasKey(name string) string {
	return "fragment:alias:" + name
}

// Set stores content under the given alias. source describes where the content
// came from (e.g. "file", "url", "stdin"). sourceRef is the original path/URL.
func (m *Manager) Set(ctx context.Context, alias, source, sourceRef string, content []byte) error {
	// Content-addressed storage
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	name := "fragment:" + hash

	artifact, err := m.store.SaveArtifact(ctx, m.workspaceID, name, content)
	if err != nil {
		return fmt.Errorf("save artifact: %w", err)
	}

	entry := AliasEntry{
		ArtifactID: artifact.ArtifactID,
		Source:     source,
		SourceRef:  sourceRef,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal alias entry: %w", err)
	}

	if err := m.store.SetState(ctx, m.sessionID, aliasKey(alias), data); err != nil {
		return fmt.Errorf("set alias state: %w", err)
	}

	return nil
}

// Get retrieves the alias entry for a fragment.
func (m *Manager) Get(ctx context.Context, alias string) (*AliasEntry, error) {
	data, err := m.store.GetState(ctx, m.sessionID, aliasKey(alias))
	if err != nil {
		return nil, fmt.Errorf("get alias state: %w", err)
	}
	if data == nil {
		return nil, fmt.Errorf("fragment %q not found", alias)
	}

	var entry AliasEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal alias entry: %w", err)
	}

	return &entry, nil
}

// Content retrieves the raw content of a fragment by alias.
func (m *Manager) Content(ctx context.Context, alias string) ([]byte, error) {
	entry, err := m.Get(ctx, alias)
	if err != nil {
		return nil, err
	}

	content, err := m.store.GetArtifact(ctx, entry.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("get artifact %s: %w", entry.ArtifactID, err)
	}

	return content, nil
}

// Remove deletes the alias mapping. The underlying artifact is not deleted
// (content-addressed storage may be shared).
func (m *Manager) Remove(ctx context.Context, alias string) error {
	// Verify it exists first
	if _, err := m.Get(ctx, alias); err != nil {
		return err
	}

	// Clear the alias by setting empty value
	return m.store.SetState(ctx, m.sessionID, aliasKey(alias), nil)
}

// List returns all known fragment aliases. Since StateStore is KV-based
// without prefix scanning, we maintain a separate index key.
func (m *Manager) List(ctx context.Context) ([]AliasListEntry, error) {
	data, err := m.store.GetState(ctx, m.sessionID, "fragment:index")
	if err != nil {
		return nil, fmt.Errorf("get fragment index: %w", err)
	}
	if data == nil {
		return nil, nil
	}

	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("unmarshal fragment index: %w", err)
	}

	var entries []AliasListEntry
	for _, name := range names {
		entry, err := m.Get(ctx, name)
		if err != nil {
			continue // skip stale entries
		}
		entries = append(entries, AliasListEntry{
			Alias: name,
			Entry: *entry,
		})
	}

	return entries, nil
}

// AliasListEntry pairs an alias name with its metadata.
type AliasListEntry struct {
	Alias string
	Entry AliasEntry
}

// updateIndex adds or removes an alias from the fragment index.
func (m *Manager) updateIndex(ctx context.Context, alias string, add bool) error {
	data, err := m.store.GetState(ctx, m.sessionID, "fragment:index")
	if err != nil {
		return err
	}

	var names []string
	if data != nil {
		_ = json.Unmarshal(data, &names)
	}

	if add {
		// Avoid duplicates
		for _, n := range names {
			if n == alias {
				return nil
			}
		}
		names = append(names, alias)
	} else {
		filtered := names[:0]
		for _, n := range names {
			if n != alias {
				filtered = append(filtered, n)
			}
		}
		names = filtered
	}

	out, err := json.Marshal(names)
	if err != nil {
		return err
	}

	return m.store.SetState(ctx, m.sessionID, "fragment:index", out)
}
