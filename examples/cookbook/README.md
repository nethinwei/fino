# fino cookbook

Recipes for the hard problems agent builders hit — each solved with **fino
primitives only**, no framework features. The runnable programs here use a tiny
in-file scripted model so they execute **offline and deterministically** (no API
key); the wiring is identical against any real provider.

```bash
go run ./examples/cookbook/hitl_resume
go run ./examples/cookbook/parallel_tools
go run ./examples/cookbook/rag_as_tool
```

| Recipe | Problem | Primitive / seam it rides on |
| --- | --- | --- |
| [`hitl_resume`](hitl_resume) | Human-in-the-loop tool approval, then continue mid-batch | `policy.Policy` gate + `runner.WithResumeFromPendingTools` |
| [`parallel_tools`](parallel_tools) | Fan out a tool batch without losing ordering | `runner.WithMaxConcurrency` |
| [`rag_as_tool`](rag_as_tool) | Retrieval-augmented generation | a retriever is just a `tool.Tool` |
| Multi-agent handoff | Route between specialist agents | `agent.NewHandoffTool` (see [`examples/multi_mode`](../multi_mode)) |
| Durable recovery | Resume after a crash | [`x/recover`](../../x/recover) (`Snapshot` = history + mode) |
| Replay & eval | Reproducible runs and regression tests | [`x/replay`](../../x/replay), [`x/eval`](../../x/eval) |

## Human-in-the-loop (HITL)

A `Policy` denies a sensitive tool; you persist the history at the dangling
`tool_use`, collect approval out of band, then resume with
`runner.WithResumeFromPendingTools()` so the already-approved call executes
before the next model turn — no checkpoint, session, or graph type. See
[`hitl_resume`](hitl_resume).

## RAG as a Tool

fino ships **no RAG pipeline** by design. Retrieval is just a tool: the model
decides when to search, the Runner executes the retriever like any other tool,
and snippets return as a `tool_result`. Swap the in-memory map in
[`rag_as_tool`](rag_as_tool) for a vector DB, BM25, or hybrid search and the loop
is unchanged. Chunking, embedding, and ranking live entirely in your tool.

## MCP as a Tool (guidance, not code)

fino has **no MCP implementation and ships no MCP adapter** — and the cookbook
intentionally includes no MCP code so nothing here is mistaken for one. If you
already run an external [MCP](https://modelcontextprotocol.io) client, you can
expose its tools to fino without any core change, because `tool.Tool` is the
only contract the Runner cares about:

1. Enumerate the MCP server's tools through your MCP client (`tools/list`).
2. For each, implement `tool.Tool`: map `Info()` to the MCP tool's name,
   description, and input schema; in `Run`, forward the JSON input to the MCP
   client's `tools/call` and return the response as a `tool.Result`.
3. Register the resulting tools on a `agent.Mode` like any hand-written tool.

The MCP transport, lifecycle, and JSON-RPC framing stay in *your* MCP client.
fino only sees ordinary tools — which is the whole point of the seam.

## The point

None of these recipes add a core abstraction. Each is a thin composition over a
primitive that already exists for the ReAct loop. That is the sufficiency
thesis in [`docs/design.md`](../../docs/design.md): reliable complex capability
from minimal primitives, precise semantics, and composition.
