package runtimecore

import (
	"fmt"
	"sync"
)

type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type StateStore struct {
	mu     sync.RWMutex
	states map[int]State
}

func NewStateStore() *StateStore {
	return &StateStore{
		states: make(map[int]State),
	}
}

func (s *StateStore) Register(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.states[id]; exists {
		return fmt.Errorf("duplicate task ID: %d", id)
	}

	s.states[id] = StatePending
	return nil
}

func validTransition(from, to State) bool {
	switch from {
	case StatePending:
		return to == StateRunning ||
			to == StateCancelled

	case StateRunning:
		return to == StateSucceeded ||
			to == StateFailed ||
			to == StateCancelled

	default:
		return false
	}
}

func (s *StateStore) Transition(
	id int,
	next State,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.states[id]
	if !exists {
		return fmt.Errorf("task %d not found", id)
	}

	if !validTransition(current, next) {
		return fmt.Errorf(
			"invalid transition: task=%d %s -> %s",
			id, current, next,
		)
	}

	s.states[id] = next
	return nil
}

func (s *StateStore) Get(id int) (State, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, exists := s.states[id]
	return state, exists
}

// 仅用于 Submit 未成功入队时撤销预登记。
func (s *StateStore) forget(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.states, id)
}
