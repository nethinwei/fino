package agui

import (
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

func TestConvertMultimodalUserInput(t *testing.T) {
	msgs := []Message{{
		ID:   "1",
		Role: RoleUser,
		Content: []any{
			map[string]any{"type": "text", "text": "what?"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://x/y.png"}},
			map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "AAA=", "format": "wav"}},
		},
	}}
	converted, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages: %v", err)
	}
	if len(converted) != 1 {
		t.Fatalf("len = %d, want 1", len(converted))
	}
	blocks := converted[0].Content
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(blocks))
	}
	if blocks[0].Type != message.TypeText || blocks[0].Text != "what?" {
		t.Fatalf("block[0] = %+v", blocks[0])
	}
	if blocks[1].Type != message.TypeImage || blocks[1].URL != "https://x/y.png" {
		t.Fatalf("block[1] = %+v", blocks[1])
	}
	if blocks[2].Type != message.TypeAudio || blocks[2].Data != "AAA=" || blocks[2].MediaType != "audio/wav" {
		t.Fatalf("block[2] = %+v", blocks[2])
	}
}

func TestConvertMultimodalUserInputDataURL(t *testing.T) {
	msgs := []Message{{
		Role: RoleUser,
		Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAA="}},
		},
	}}
	converted, err := convertMessages(msgs)
	if err != nil {
		t.Fatalf("convertMessages: %v", err)
	}
	b := converted[0].Content[0]
	if b.Type != message.TypeImage || b.SourceType != message.SourceBase64 || b.Data != "AAA=" || b.MediaType != "image/png" {
		t.Fatalf("block = %+v", b)
	}
}

func TestMapTurnMessageMultimodalOutput(t *testing.T) {
	mapper, _ := NewMapper("thread", "run")
	msg := message.Assistant(
		message.NewText("here"),
		message.NewImage("image/png", message.WithURL("https://x/y.png")),
	)
	events, err := mapper.Map(model.TurnMessage{Message: msg})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	var custom *CustomEvent
	for i := range events {
		if ce, ok := events[i].(CustomEvent); ok {
			custom = &ce
			break
		}
	}
	if custom == nil {
		t.Fatalf("no multimodal_block CustomEvent in %+v", events)
	}
	if custom.Name != "multimodal_block" {
		t.Fatalf("name = %q", custom.Name)
	}
}

func TestValidateAssistantBlocksAcceptsMultimodal(t *testing.T) {
	blocks := []message.Block{
		message.NewImage("image/png", message.WithURL("https://x/y.png")),
		message.NewAudio("audio/wav", message.WithBase64("AAA=")),
	}
	if err := validateAssistantBlocks(blocks); err != nil {
		t.Fatalf("should accept multimodal: %v", err)
	}
}

type capableStreamModel struct{ streamModel }

func (capableStreamModel) Capabilities() model.CapabilitiesInfo {
	return model.CapabilitiesInfo{
		InputModalities:  []message.BlockType{message.TypeText, message.TypeImage},
		OutputModalities: []message.BlockType{message.TypeText},
	}
}

func TestCapabilitiesReportsModalities(t *testing.T) {
	rt := makeRuntime(t, &capableStreamModel{}, makeAgent(t))
	caps := rt.Capabilities()
	if !containsString(caps.InputModalities, "image") {
		t.Fatalf("input modalities missing image: %+v", caps.InputModalities)
	}
	if !containsString(caps.OutputModalities, "text") {
		t.Fatalf("output modalities missing text: %+v", caps.OutputModalities)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
