package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"hop.top/wsm/pkg/backend/sqlite"
	"hop.top/wsm/pkg/workspace"
)

func InitWorkspace(ctx context.Context) (*workspace.Manager, *workspace.Workspace, error) {
	// Use a default path for the SQLite backend
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	dbPath := filepath.Join(home, ".config", "foo", "foo.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, nil, err
	}

	b, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}

	// Ensure the database is migrated
	if err := b.Migrate(ctx); err != nil {
		return nil, nil, fmt.Errorf("migrate workspace db: %w", err)
	}

	m, err := workspace.NewManager(b)
	if err != nil {
		return nil, nil, err
	}

	// Resolve workspace based on current directory
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	wsName := filepath.Base(cwd)

	ws, err := m.ResolveWorkspaceRef(ctx, wsName)
	if err != nil {
		// Create if not found
		ws, err = m.Create(ctx, wsName, workspace.CreateOptions{
			Description: fmt.Sprintf("foo workspace for %s", wsName),
		})
		if err != nil {
			return nil, nil, err
		}
	}

	return m, ws, nil
}
