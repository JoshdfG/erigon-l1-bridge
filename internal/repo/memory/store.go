// Package memory provides in-memory implementations of the usecase repositories.
// Use in tests and the local devnet — no DB required.
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

var _ usecase.ClearingRepository = (*ClearingStore)(nil)
var _ usecase.EventRepository = (*EventStore)(nil)

// ─── ClearingStore ────────────────────────────────────────────────────────────

type ClearingStore struct {
	mu   sync.RWMutex
	data map[string]entity.ClearingIntent
}

func NewClearingStore() *ClearingStore {
	return &ClearingStore{data: make(map[string]entity.ClearingIntent)}
}

func (s *ClearingStore) Save(_ context.Context, intent entity.ClearingIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[intent.ID] = intent
	return nil
}

func (s *ClearingStore) GetByID(_ context.Context, id string) (*entity.ClearingIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("intent %s not found", id)
	}
	return &i, nil
}

// GetPending returns all intents that are not yet in a terminal state.
// Terminal states: StatusFinalized, StatusFailed.
func (s *ClearingStore) GetPending(_ context.Context) ([]entity.ClearingIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []entity.ClearingIntent
	for _, i := range s.data {
		switch i.Event.Status {
		case entity.StatusFinalized, entity.StatusFailed:
			// terminal — skip
		default:
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *ClearingStore) UpdateStatus(_ context.Context, id string, status entity.BridgeStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.data[id]
	if !ok {
		return fmt.Errorf("intent %s not found", id)
	}
	i.Event.Status = status
	s.data[id] = i
	return nil
}

// ─── EventStore ───────────────────────────────────────────────────────────────

type EventStore struct {
	mu   sync.RWMutex
	data map[string]entity.BridgeEvent
}

func NewEventStore() *EventStore {
	return &EventStore{data: make(map[string]entity.BridgeEvent)}
}

func (s *EventStore) Save(_ context.Context, event entity.BridgeEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[event.ID] = event
	return nil
}

func (s *EventStore) GetByID(_ context.Context, id string) (*entity.BridgeEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[id]
	if !ok {
		return nil, fmt.Errorf("event %s not found", id)
	}
	return &e, nil
}

func (s *EventStore) GetByStatus(_ context.Context, status entity.BridgeStatus) ([]entity.BridgeEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []entity.BridgeEvent
	for _, e := range s.data {
		if e.Status == status {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *EventStore) UpdateStatus(_ context.Context, id string, status entity.BridgeStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[id]
	if !ok {
		return fmt.Errorf("event %s not found", id)
	}
	e.Status = status
	s.data[id] = e
	return nil
}
