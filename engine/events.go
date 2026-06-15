package engine

import "time"

// EventType is the type of an execution event.
type EventType string

const (
	EventStarted  EventType = "started"
	EventFinished EventType = "finished"
)

// Event is a single execution event. Emitted by the engine around run
// operations and by the units scheduler around unit operations; reporting
// does not depend on the reporter operation.
type Event struct {
	Type       EventType `json:"event"`
	Time       time.Time `json:"time"`
	RunID      string    `json:"run_id"`
	Op         string    `json:"op"`
	Unit       string    `json:"unit,omitempty"`
	Status     string    `json:"status,omitempty"`      // finished only
	DurationMs int64     `json:"duration_ms,omitempty"` // finished only
	Error      string    `json:"error,omitempty"`
	Output     string    `json:"output,omitempty"` // truncated operation output
}

// MaxEventOutput caps the size of operation output embedded in an event.
const MaxEventOutput = 4 * 1024

// EventSink consumes events. Implementations must be thread-safe:
// unit operations emit events from parallel goroutines.
type EventSink interface {
	Emit(Event)
}

// NopSink is a no-op sink for tests and cases where events are not needed.
type NopSink struct{}

func (NopSink) Emit(Event) {}
