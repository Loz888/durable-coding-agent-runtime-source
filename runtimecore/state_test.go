package runtimecore

import "testing"

func TestStateLifecycle(t *testing.T) {
	s := NewStateStore()

	if err := s.Register(1); err != nil {
		t.Fatal(err)
	}

	state, ok := s.Get(1)
	if !ok || state != StatePending {
		t.Fatalf("expected pending, got %s", state)
	}

	if err := s.Transition(1, StateRunning); err != nil {
		t.Fatal(err)
	}

	if err := s.Transition(1, StateSucceeded); err != nil {
		t.Fatal(err)
	}

	state, ok = s.Get(1)
	if !ok || state != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", state)
	}
}

// TestInvalidTransition verifies that a terminal state cannot return to running.
func TestInvalidTransition(t *testing.T) {
	s := NewStateStore()
	if err := s.Register(2); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := s.Transition(2, StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(2, StateSucceeded); err != nil {
		t.Fatal(err)
	}

	if err := s.Transition(2, StateRunning); err == nil {
		t.Fatal("expected invalid transition error")
	}

	state, ok := s.Get(2)
	if !ok {
		t.Fatal("task not found")
	}
	if state != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", state)
	}
}

// TestDuplicateTaskID verifies that duplicate registration preserves the state.
func TestDuplicateTaskID(t *testing.T) {
	s := NewStateStore()
	if err := s.Register(3); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if err := s.Transition(3, StateRunning); err != nil {
		t.Fatal(err)
	}

	if err := s.Register(3); err == nil {
		t.Fatal("expected duplicate task ID error")
	}

	state, ok := s.Get(3)
	if !ok {
		t.Fatal("original task not found")
	}
	if state != StateRunning {
		t.Fatalf("original state was overwritten: got %s, want %s", state, StateRunning)
	}
}
