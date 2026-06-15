package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tufitko/oink/engine"
)

func TestNDJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	sink := NewNDJSON(&buf)
	sink.Emit(engine.Event{Type: engine.EventStarted, Time: time.Now(), RunID: "r1", Op: "hello"})
	sink.Emit(engine.Event{Type: engine.EventFinished, Time: time.Now(), RunID: "r1", Op: "hello", Unit: "db", Status: "ok", DurationMs: 5})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 0 is not valid JSON: %v", err)
	}
	if first["event"] != "started" || first["op"] != "hello" {
		t.Fatalf("unexpected first event: %v", first)
	}
	if _, hasUnit := first["unit"]; hasUnit {
		t.Fatal("empty unit must be omitted")
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second["unit"] != "db" || second["status"] != "ok" {
		t.Fatalf("unexpected second event: %v", second)
	}
}

func TestNDJSONConcurrentEmit(t *testing.T) {
	var buf bytes.Buffer
	sink := NewNDJSON(&buf)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sink.Emit(engine.Event{Type: engine.EventStarted, Op: "op"})
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines, want 50", len(lines))
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Fatalf("interleaved/corrupted line: %q", l)
		}
	}
}
