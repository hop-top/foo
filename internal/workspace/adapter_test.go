package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// TestAdapter is an in-memory implementation of the workspace.Store for testing.
type TestAdapter struct {
	mu        sync.RWMutex
	counter   atomic.Int64
	sessions  map[string][]string // workspaceID -> sessionIDs
	events    map[string][]Event  // sessionID -> events
	artifacts map[string][]byte   // artifactID -> content
	states    map[string]map[string][]byte
}

func NewTestAdapter() *TestAdapter {
	return &TestAdapter{
		sessions:  make(map[string][]string),
		events:    make(map[string][]Event),
		artifacts: make(map[string][]byte),
		states:    make(map[string]map[string][]byte),
	}
}

func (a *TestAdapter) CreateSession(ctx context.Context, workspaceID string, metadata map[string]string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := fmt.Sprintf("test-session-%d", a.counter.Add(1))
	a.sessions[workspaceID] = append(a.sessions[workspaceID], id)
	return id, nil
}

func (a *TestAdapter) ListSessions(ctx context.Context, workspaceID string) ([]string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessions[workspaceID], nil
}

func (a *TestAdapter) LogEvent(ctx context.Context, event Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events[event.SessionID] = append(a.events[event.SessionID], event)
	return nil
}

func (a *TestAdapter) GetSessionEvents(ctx context.Context, workspaceID string, sessionID string) ([]Event, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.events[sessionID], nil
}

func (a *TestAdapter) SearchEvents(ctx context.Context, workspaceID string, query string) ([]Event, error) {
	return nil, nil
}

func (a *TestAdapter) SaveArtifact(ctx context.Context, workspaceID string, name string, content []byte) (*ArtifactData, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := sha256.Sum256(content)
	id := fmt.Sprintf("%x", h[:8])
	a.artifacts[id] = content
	return &ArtifactData{ArtifactID: id, Path: name}, nil
}

func (a *TestAdapter) GetArtifact(ctx context.Context, artifactID string) ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c, ok := a.artifacts[artifactID]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

func (a *TestAdapter) SetState(ctx context.Context, sessionID string, key string, value []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.states[sessionID]; !ok {
		a.states[sessionID] = make(map[string][]byte)
	}
	a.states[sessionID][key] = value
	return nil
}

func (a *TestAdapter) GetState(ctx context.Context, sessionID string, key string) ([]byte, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if s, ok := a.states[sessionID]; ok {
		return s[key], nil
	}
	return nil, nil
}
