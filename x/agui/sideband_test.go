package agui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStateSnapshotEventJSONShape(t *testing.T) {
	ev := NewStateSnapshotEvent(map[string]any{"count": float64(2)})
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"STATE_SNAPSHOT"`,
		`"snapshot":{"count":2}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestStateDeltaEventZeroOpsSerializesAsEmptyArray(t *testing.T) {
	ev := NewStateDeltaEvent()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"delta":[]`) {
		t.Fatalf("json = %s, want delta to be [] not null", got)
	}
	ev2 := NewActivityDeltaEvent("act_1", "search")
	data2, err := json.Marshal(ev2)
	if err != nil {
		t.Fatalf("Marshal ActivityDelta error: %v", err)
	}
	if !strings.Contains(string(data2), `"patch":[]`) {
		t.Fatalf("json = %s, want patch to be [] not null", string(data2))
	}
}

func TestStateDeltaEventUsesJSONPatch(t *testing.T) {
	ev := NewStateDeltaEvent(
		JSONPatchOp{Op: "replace", Path: "/count", Value: float64(3)},
		JSONPatchOp{Op: "remove", Path: "/draft"},
	)
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"STATE_DELTA"`,
		`"delta":[`,
		`"op":"replace"`,
		`"path":"/count"`,
		`"value":3`,
		`"op":"remove"`,
		`"path":"/draft"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestJSONPatchOpOmitsEmptyValueAndFrom(t *testing.T) {
	data, err := json.Marshal(JSONPatchOp{Op: "remove", Path: "/x"})
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	if strings.Contains(got, `"value"`) {
		t.Fatalf("json = %s, should omit value when absent", got)
	}
	if strings.Contains(got, `"from"`) {
		t.Fatalf("json = %s, should omit from when absent", got)
	}
}

func TestActivitySnapshotEventJSONShape(t *testing.T) {
	ev := NewActivitySnapshotEvent("act_1", "search", map[string]any{"step": "querying"})
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"ACTIVITY_SNAPSHOT"`,
		`"messageId":"act_1"`,
		`"activityType":"search"`,
		`"content":{"step":"querying"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestActivityDeltaEventUsesJSONPatch(t *testing.T) {
	ev := NewActivityDeltaEvent("act_1", "search",
		JSONPatchOp{Op: "add", Path: "/results/-", Value: "hit"},
	)
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"ACTIVITY_DELTA"`,
		`"messageId":"act_1"`,
		`"activityType":"search"`,
		`"patch":[`,
		`"op":"add"`,
		`"path":"/results/-"`,
		`"value":"hit"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestRawEventJSONShape(t *testing.T) {
	src := "provider"
	ev := NewRawEvent(map[string]any{"key": "val"}, &src)
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"RAW"`,
		`"event":{"key":"val"}`,
		`"source":"provider"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

func TestCustomEventJSONShape(t *testing.T) {
	ev := NewCustomEvent("agent_step", map[string]any{"step": float64(1)})
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"type":"CUSTOM"`,
		`"name":"agent_step"`,
		`"value":{"step":1}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("json = %s, want %s", got, want)
		}
	}
}

// TestSideBandRolesDoNotEnterModelHistory pins success criterion 2: activity and
// reasoning side-band messages a client may echo back are not converted into
// fino model history, so they cannot influence a subsequent run.
func TestSideBandRolesDoNotEnterModelHistory(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleActivity, ActivityType: "search", Content: "querying"},
		{Role: RoleReasoning, Content: "thinking out loud"},
	}
	got, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("converted %d messages, want 1 (only the user turn)", len(got))
	}
	if got[0].Role != "user" {
		t.Fatalf("converted role = %q, want user", got[0].Role)
	}
}
