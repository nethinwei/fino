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
	if decision.ResolvedKind() != DecisionAllow {
		t.Fatalf("ResolvedKind = %v, want DecisionAllow", decision.ResolvedKind())
	}
}

func TestResolvedKind(t *testing.T) {
	cases := []struct {
		name string
		d    Decision
		want DecisionKind
	}{
		{"explicit allow", Decision{Kind: DecisionAllow}, DecisionAllow},
		{"explicit deny", Decision{Kind: DecisionDeny}, DecisionDeny},
		{"explicit suspend", Decision{Kind: DecisionSuspend}, DecisionSuspend},
		{"legacy allow true", Decision{Allow: true}, DecisionAllow},
		{"legacy allow false", Decision{Allow: false}, DecisionDeny},
		{"kind wins over allow", Decision{Kind: DecisionAllow, Allow: false}, DecisionAllow},
	}
	for _, c := range cases {
		if got := c.d.ResolvedKind(); got != c.want {
			t.Errorf("%s: ResolvedKind = %v, want %v", c.name, got, c.want)
		}
	}
}
