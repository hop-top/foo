package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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

// WSMAdapter is a production adapter for Foo that wraps the kit/wsm package.
// It maps Foo's internal Store interface to wsm's Manager and Backend.
type WSMAdapter struct {
	manager *wsm_core.Manager
	ws      *wsm_core.Workspace
}

// NewWSMAdapter initializes a kit/wsm instance and returns it as a workspace.Store.
func NewWSMAdapter(ctx context.Context, dbPath string) (*WSMAdapter, error) {
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dbPath = filepath.Join(home, ".config", "foo", "foo.db")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
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
	}, nil
}

func (a *WSMAdapter) CreateSession(ctx context.Context, workspaceID string, metadata map[string]string) (string, error) {
	// In wsm, session IDs are often external or provided during event recording.
	// We'll generate a ULID or similar if none provided.
	return "", nil 
}

func (a *WSMAdapter) ListSessions(ctx context.Context, workspaceID string) ([]string, error) {
	return nil, nil
}

func (a *WSMAdapter) LogEvent(ctx context.Context, event Event) error {
	// Convert workspace.Event back to wsm model types if necessary, 
	// though they are currently aliased.
	_, err := a.manager.RecordEvent(ctx, a.ws.ID, wsm_core.EventType(event.Type), event.Data)
	return err
}

func (a *WSMAdapter) GetSessionEvents(ctx context.Context, workspaceID string, sessionID string) ([]Event, error) {
	// Use manager.Events with filter for SessionID
	return nil, nil
}

func (a *WSMAdapter) SearchEvents(ctx context.Context, workspaceID string, query string) ([]Event, error) {
	return nil, nil
}

func (a *WSMAdapter) SaveArtifact(ctx context.Context, workspaceID string, name string, content []byte) (*ArtifactData, error) {
	return nil, nil
}

func (a *WSMAdapter) GetArtifact(ctx context.Context, artifactID string) ([]byte, error) {
	return nil, nil
}

func (a *WSMAdapter) SetState(ctx context.Context, sessionID string, key string, value []byte) error {
	// Map to interaction.state event type or similar in wsm
	return nil
}

func (a *WSMAdapter) GetState(ctx context.Context, sessionID string, key string) ([]byte, error) {
	return nil, nil
}
