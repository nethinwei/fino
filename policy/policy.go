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

// DecisionKind is the three-state outcome of an authorization check.
type DecisionKind uint8

const (
	// DecisionUnspecified is the zero value. It means the Policy did not set
	// Kind, so the Runner falls back to the legacy Allow bool (see ResolvedKind).
	DecisionUnspecified DecisionKind = iota
	// DecisionAllow permits the tool to execute.
	DecisionAllow
	// DecisionDeny rejects the tool; the Runner fails the run with ToolDeniedError.
	DecisionDeny
	// DecisionSuspend halts the run so a human can approve the call. The Run
	// path returns a suspended Result; suspend is not a runtime error.
	DecisionSuspend
)

// Decision is the result of an authorization check. New code should set Kind.
// The Allow field is soft-deprecated: it is honored only when Kind is
// DecisionUnspecified, via the migration rule in ResolvedKind.
type Decision struct {
	// Kind is the three-state decision. Prefer setting this over Allow.
	Kind DecisionKind
	// Allow is the legacy binary decision. It is read only when Kind is
	// DecisionUnspecified. New implementations should leave it at its zero value.
	Allow  bool
	Reason string
}

// ResolvedKind returns the effective decision kind. If Kind is set (not
// DecisionUnspecified) it is returned directly; otherwise the legacy Allow bool
// is mapped (true -> DecisionAllow, false -> DecisionDeny). This single rule
// keeps existing binary policies working without modification.
func (d Decision) ResolvedKind() DecisionKind {
	if d.Kind != DecisionUnspecified {
		return d.Kind
	}
	if d.Allow {
		return DecisionAllow
	}
	return DecisionDeny
}

// AllowAll is a Policy that allows every tool invocation.
type AllowAll struct{}

// Authorize allows all tool invocations unconditionally.
func (AllowAll) Authorize(ctx context.Context, req Request) (Decision, error) {
	return Decision{Kind: DecisionAllow}, nil
}
