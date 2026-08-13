package message

import (
	"encoding/json"
	"testing"
)

func TestUsageJSONOmitempty(t *testing.T) {
	m := Assistant(NewText("hi"))
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"role":"assistant","content":[{"type":"text","text":"hi"}]}` {
		t.Fatalf("unexpected json: %s", data)
	}
}

func TestUsageJSONRoundtrip(t *testing.T) {
	m := Assistant(NewText("hi"))
	m.Usage = &Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 80, CacheWriteTokens: 20}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Usage == nil {
		t.Fatal("usage was not round-tripped")
	}
	if back.Usage.InputTokens != 100 || back.Usage.OutputTokens != 50 ||
		back.Usage.CacheReadTokens != 80 || back.Usage.CacheWriteTokens != 20 {
		t.Fatalf("usage = %+v", back.Usage)
	}
}
