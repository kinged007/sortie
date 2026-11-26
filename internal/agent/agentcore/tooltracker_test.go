package agentcore

import (
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestToolTracker_BeginEnd_Roundtrip(t *testing.T) {
	t.Parallel()

	tracker := NewToolTracker()
	want := ToolCall{Name: "Read", Kind: domain.ToolKindRead, Argument: "main.go"}
	tracker.Begin("id-1", want)

	call, durationMS, ok := tracker.End("id-1")
	if !ok {
		t.Fatal("End(\"id-1\") ok = false, want true")
	}
	if call != want {
		t.Errorf("End(\"id-1\") call = %+v, want %+v", call, want)
	}
	if durationMS < 0 {
		t.Errorf("End(\"id-1\") durationMS = %d, want >= 0", durationMS)
	}
}

func TestToolTracker_End_UnknownID(t *testing.T) {
	t.Parallel()

	tracker := NewToolTracker()

	call, durationMS, ok := tracker.End("nonexistent")
	if ok {
		t.Fatal("End(\"nonexistent\") ok = true, want false")
	}
	if call != (ToolCall{}) {
		t.Errorf("End(\"nonexistent\") call = %+v, want zero", call)
	}
	if durationMS != 0 {
		t.Errorf("End(\"nonexistent\") durationMS = %d, want 0", durationMS)
	}
}

func TestToolTracker_Begin_Overwrite(t *testing.T) {
	t.Parallel()

	tracker := NewToolTracker()
	tracker.Begin("id-x", ToolCall{Name: "OldTool"})
	tracker.Begin("id-x", ToolCall{Name: "NewTool"})

	call, _, ok := tracker.End("id-x")
	if !ok {
		t.Fatal("End(\"id-x\") ok = false, want true")
	}
	if call.Name != "NewTool" {
		t.Errorf("End(\"id-x\") name = %q, want %q", call.Name, "NewTool")
	}
}

func TestToolTracker_End_Idempotency(t *testing.T) {
	t.Parallel()

	tracker := NewToolTracker()
	tracker.Begin("id-once", ToolCall{Name: "Bash"})

	_, _, first := tracker.End("id-once")
	if !first {
		t.Fatal("first End(\"id-once\") ok = false, want true")
	}

	_, durationMS, second := tracker.End("id-once")
	if second {
		t.Error("second End(\"id-once\") ok = true, want false (idempotent)")
	}
	if durationMS != 0 {
		t.Errorf("second End durationMS = %d, want 0", durationMS)
	}
}

func TestToolTracker_MultipleEntries(t *testing.T) {
	t.Parallel()

	tracker := NewToolTracker()
	tracker.Begin("tool-a", ToolCall{Name: "ToolA"})
	tracker.Begin("tool-b", ToolCall{Name: "ToolB"})
	tracker.Begin("tool-c", ToolCall{Name: "ToolC"})

	tests := []struct {
		id       string
		wantName string
	}{
		{"tool-a", "ToolA"},
		{"tool-b", "ToolB"},
		{"tool-c", "ToolC"},
	}

	// ToolTracker is not goroutine-safe, so subtests sharing it stay serial.
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			call, _, ok := tracker.End(tt.id)
			if !ok {
				t.Fatalf("End(%q) ok = false, want true", tt.id)
			}
			if call.Name != tt.wantName {
				t.Errorf("End(%q) name = %q, want %q", tt.id, call.Name, tt.wantName)
			}
		})
	}
}

func TestToolTracker_DurationClamp(t *testing.T) {
	t.Parallel()

	tracker := NewToolTracker()
	tracker.entries["id-future"] = toolEntry{
		call: ToolCall{Name: "Bash"},
		ts:   time.Now().Add(1 * time.Hour),
	}

	call, durationMS, ok := tracker.End("id-future")
	if !ok {
		t.Fatal("End(\"id-future\") ok = false, want true")
	}
	if call.Name != "Bash" {
		t.Errorf("End(\"id-future\") name = %q, want %q", call.Name, "Bash")
	}
	if durationMS != 0 {
		t.Errorf("End(\"id-future\") durationMS = %d, want 0 (clamp for non-positive duration)", durationMS)
	}
}
