// Package events — engine.EventSink implementations. The default is NDJSON
// to stdout.
package events

import (
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/tufitko/oink/engine"
)

// NDJSON is a thread-safe NDJSON emitter: one event — one JSON line.
type NDJSON struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewNDJSON writes events to w. NewNDJSON(nil) writes to os.Stdout.
func NewNDJSON(w io.Writer) *NDJSON {
	if w == nil {
		w = os.Stdout
	}
	return &NDJSON{enc: json.NewEncoder(w)}
}

func (s *NDJSON) Emit(e engine.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An event write failure must not bring the pipeline down — ignore it.
	_ = s.enc.Encode(e)
}
