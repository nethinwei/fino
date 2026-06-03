package agui

import (
	"context"
	"sync"

	"github.com/nethinwei/fino/runner"
)

// SuspendStore persists the suspend snapshot a run produced so that a later
// resume request restores the exact runner.SuspendedRun captured when a Policy
// suspended the run, instead of trusting the client-supplied message history. A
// resume that cannot find a stored snapshot must be rejected: this is what
// prevents a caller from forging messages to execute a tool the Policy never
// authorized. Snapshots are keyed by AG-UI thread ID, matching AG-UI's serial
// run model where a thread has at most one outstanding interrupt at a time.
type SuspendStore interface {
	// Save stores the snapshot for a thread, replacing any prior snapshot.
	Save(ctx context.Context, threadID string, snapshot runner.SuspendedRun) error
	// Load returns the stored snapshot and whether one exists for the thread.
	Load(ctx context.Context, threadID string) (runner.SuspendedRun, bool, error)
	// Delete removes the snapshot for a thread, if any.
	Delete(ctx context.Context, threadID string) error
}

// InMemorySuspendStore is a goroutine-safe in-process SuspendStore. It is
// suitable for single-process deployments and tests; durable or multi-process
// deployments should supply their own SuspendStore.
type InMemorySuspendStore struct {
	mu        sync.Mutex
	snapshots map[string]runner.SuspendedRun
}

// NewInMemorySuspendStore creates an empty in-memory store.
func NewInMemorySuspendStore() *InMemorySuspendStore {
	return &InMemorySuspendStore{snapshots: make(map[string]runner.SuspendedRun)}
}

// Save stores the snapshot for a thread.
func (s *InMemorySuspendStore) Save(_ context.Context, threadID string, snapshot runner.SuspendedRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[threadID] = snapshot
	return nil
}

// Load returns the stored snapshot for a thread, if present.
func (s *InMemorySuspendStore) Load(_ context.Context, threadID string) (runner.SuspendedRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[threadID]
	return snapshot, ok, nil
}

// Delete removes the snapshot for a thread.
func (s *InMemorySuspendStore) Delete(_ context.Context, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snapshots, threadID)
	return nil
}
