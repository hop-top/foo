package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"hop.top/kit/go/core/xdg"
	wsm_sqlite "hop.top/wsm/pkg/backend/sqlite"
	wsm_core "hop.top/wsm/pkg/workspace"
)

// InitWorkspace creates a WSMAdapter with default paths and returns the
// underlying manager + workspace for direct event recording.
func InitWorkspace(ctx context.Context) (*wsm_core.Manager, *wsm_core.Workspace, error) {
	a, err := NewWSMAdapter(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	return a.manager, a.ws, nil
}

// WSMAdapter is a production adapter for Foo that wraps hop.top/wsm.
// It maps Foo's internal Store interface to wsm's Manager and Backend.
type WSMAdapter struct {
	manager *wsm_core.Manager
	ws      *wsm_core.Workspace
	rootDir string
}

// NewWSMAdapter initializes a hop.top/wsm-backed adapter and returns it as a workspace.Store.
func NewWSMAdapter(ctx context.Context, dbPath string) (*WSMAdapter, error) {
	stateDir, err := xdg.StateDir("foo")
	if err != nil {
		return nil, err
	}
	if dbPath == "" {
		dbPath = filepath.Join(stateDir, "workspace.db")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	rootDir := filepath.Join(stateDir, "workspace-store")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, err
	}

	backend, err := wsm_sqlite.Open(dbPath)
	if err != nil {
		return nil, err
	}

	if err := backend.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate wsm db: %w", err)
	}

	manager, err := wsm_core.NewManager(backend)
	if err != nil {
		return nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	wsName := filepath.Base(cwd)

	ws, err := manager.ResolveWorkspaceRef(ctx, wsName)
	if err != nil {
		ws, err = manager.Create(ctx, wsName, wsm_core.CreateOptions{
			Description: fmt.Sprintf("foo workspace for %s", wsName),
		})
		if err != nil {
			return nil, err
		}
	}

	return &WSMAdapter{
		manager: manager,
		ws:      ws,
		rootDir: rootDir,
	}, nil
}

func (a *WSMAdapter) CreateSession(ctx context.Context, workspaceID string, metadata map[string]string) (string, error) {
	return "", fmt.Errorf("WSMAdapter.CreateSession not implemented")
}

func (a *WSMAdapter) ListSessions(ctx context.Context, workspaceID string) ([]string, error) {
	return nil, fmt.Errorf("WSMAdapter.ListSessions not implemented")
}

func (a *WSMAdapter) LogEvent(ctx context.Context, event Event) error {
	// Convert workspace.Event back to wsm model types if necessary, 
	// though they are currently aliased.
	_, err := a.manager.RecordEvent(ctx, a.ws.ID, wsm_core.EventType(event.Type), event.Data)
	return err
}

func (a *WSMAdapter) GetSessionEvents(ctx context.Context, workspaceID string, sessionID string) ([]Event, error) {
	return nil, fmt.Errorf("WSMAdapter.GetSessionEvents not implemented")
}

func (a *WSMAdapter) SearchEvents(ctx context.Context, workspaceID string, query string) ([]Event, error) {
	return nil, fmt.Errorf("WSMAdapter.SearchEvents not implemented")
}

func (a *WSMAdapter) SaveArtifact(ctx context.Context, workspaceID string, name string, content []byte) (*ArtifactData, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	artifactDir := filepath.Join(a.rootDir, "artifacts")
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(artifactDir, hash)
	if err := os.WriteFile(path, content, 0644); err != nil {
		return nil, err
	}
	payload := ArtifactData{
		ArtifactID: hash,
		Type:       "blob",
		Path:       name,
		Hash:       hash,
		Size:       int64(len(content)),
	}
	_, _ = a.manager.RecordEvent(ctx, a.ws.ID, wsm_core.EventMutationArtifact, payload)
	return &payload, nil
}

func (a *WSMAdapter) GetArtifact(ctx context.Context, artifactID string) ([]byte, error) {
	path := filepath.Join(a.rootDir, "artifacts", artifactID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artifact %q not found", artifactID)
		}
		return nil, err
	}
	return data, nil
}

func (a *WSMAdapter) SetState(ctx context.Context, sessionID string, key string, value []byte) error {
	path, err := a.statePath(sessionID, key)
	if err != nil {
		return err
	}
	if value == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, value, 0644)
}

func (a *WSMAdapter) GetState(ctx context.Context, sessionID string, key string) ([]byte, error) {
	path, err := a.statePath(sessionID, key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (a *WSMAdapter) statePath(sessionID string, key string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session ID is required")
	}
	if key == "" {
		return "", fmt.Errorf("state key is required")
	}
	filename := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	return filepath.Join(a.rootDir, "state", sessionID, filename), nil
}
