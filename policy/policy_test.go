package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nethinwei/fino/tool"
)

func TestAllowAll(t *testing.T) {
	decision, err := AllowAll{}.Authorize(context.Background(), Request{
		Tool:  tool.Info{Name: "read"},
		Input: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Authorize error: %v", err)
	}
	if !decision.Allow {
		t.Fatalf("Allow = false, reason = %q", decision.Reason)
	}
}
