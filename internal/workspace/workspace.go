package workspace

import (
	"context"

	"hop.top/wsm/pkg/model"
)

// Re-export types from wsm model for convenience and native alignment.
type Event = model.Event
type MessageData = model.MessageData
type ToolCallData = model.ToolCallData
type ArtifactData = model.ArtifactData
type Workspace = model.Workspace

// Store is the primary interface for Foo's interaction with wsm.
// It uses wsm's native types to ensure universal session compatibility.
type Store interface {
	SessionStore
	EventStore
	ArtifactStore
	StateStore
}

// SessionStore manages conversation sessions within a workspace.
type SessionStore interface {
	// CreateSession creates a logical session ID for a sequence of events.
	CreateSession(ctx context.Context, workspaceID string, metadata map[string]string) (string, error)
	ListSessions(ctx context.Context, workspaceID string) ([]string, error)
}

// EventStore handles the event-sourced log of interactions.
type EventStore interface {
	// LogEvent appends a native wsm event to the log.
	LogEvent(ctx context.Context, event Event) error
	// GetSessionEvents retrieves all events for a specific session.
	GetSessionEvents(ctx context.Context, workspaceID string, sessionID string) ([]Event, error)
	// SearchEvents performs a full-text search across events in a workspace.
	SearchEvents(ctx context.Context, workspaceID string, query string) ([]Event, error)
}

// ArtifactStore handles content-addressed storage (Fragments).
type ArtifactStore interface {
	// SaveArtifact stores a blob and returns its ArtifactData (including Hash/ID).
	SaveArtifact(ctx context.Context, workspaceID string, name string, content []byte) (*ArtifactData, error)
	GetArtifact(ctx context.Context, artifactID string) ([]byte, error)
}

// StateStore provides KV persistence for stateful toolboxes.
type StateStore interface {
	SetState(ctx context.Context, sessionID string, key string, value []byte) error
	GetState(ctx context.Context, sessionID string, key string) ([]byte, error)
}
