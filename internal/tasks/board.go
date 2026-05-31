// Package tasks holds the per-session task list the agent maintains via the
// tasks_set tool. The Board is process-shared (one per agent instance); the
// CLI subscribes to push updates into the bubbletea program.
package tasks

import "sync"

// Status is the lifecycle a task moves through.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// Task is a single agent-managed work item.
type Task struct {
	Title  string `json:"title"`
	Status Status `json:"status"`
}

// Board holds the current task list. Set replaces it wholesale (TodoWrite
// semantics); subscribers are notified after the mutex is released so a slow
// listener never blocks the agent.
type Board struct {
	mu        sync.Mutex
	tasks     []Task
	listeners []func([]Task)
}

// NewBoard constructs an empty Board.
func NewBoard() *Board { return &Board{} }

// Set replaces the entire task list and notifies listeners with a snapshot.
func (b *Board) Set(tasks []Task) {
	b.mu.Lock()
	b.tasks = append(b.tasks[:0], tasks...)
	snapshot := append([]Task(nil), b.tasks...)
	listeners := make([]func([]Task), len(b.listeners))
	copy(listeners, b.listeners)
	b.mu.Unlock()
	for _, fn := range listeners {
		fn(snapshot)
	}
}

// Snapshot returns a copy of the current task list.
func (b *Board) Snapshot() []Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Task(nil), b.tasks...)
}

// Subscribe registers a listener invoked after each Set. The returned func
// removes the listener (currently unused but kept so callers can clean up).
func (b *Board) Subscribe(fn func([]Task)) {
	b.mu.Lock()
	b.listeners = append(b.listeners, fn)
	b.mu.Unlock()
}

// Count returns the number of tasks. Cheap for the status row.
func (b *Board) Count() (total, done, inProgress int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total = len(b.tasks)
	for _, t := range b.tasks {
		switch t.Status {
		case StatusCompleted:
			done++
		case StatusInProgress:
			inProgress++
		}
	}
	return total, done, inProgress
}
