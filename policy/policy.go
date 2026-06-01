// Package policy defines the authorization interface for tool execution in the
// fino Agent SDK. A Policy is consulted before each tool call; it can allow or
// deny the invocation.
package policy

import (
	"context"
	"encoding/json"

	"github.com/nethinwei/fino/tool"
)

// Policy is the authorization interface consulted before each tool execution.
// Implementations can enforce confirmation, RBAC, audit, sandbox, allowlist, or
// risk-scoring rules.
type Policy interface {
	// Authorize decides whether to allow a tool invocation. A returned error
	// indicates the policy system itself failed (e.g. remote service timeout),
	// which is distinct from a deny decision.
	Authorize(ctx context.Context, req Request) (Decision, error)
}

// Request describes the tool invocation being authorized.
type Request struct {
	AgentName string
	ModeName  string
	Tool      tool.Info
	Input     json.RawMessage
}

// Decision is the result of an authorization check.
type Decision struct {
	Allow  bool
	Reason string
}

// AllowAll is a Policy that allows every tool invocation.
type AllowAll struct{}

// Authorize allows all tool invocations unconditionally.
func (AllowAll) Authorize(ctx context.Context, req Request) (Decision, error) {
	return Decision{Allow: true}, nil
}
