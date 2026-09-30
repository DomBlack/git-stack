# MCP server

`git stack mcp` runs a stdio MCP server exposing the stack operations to coding agents.
Tools, schemas and error shapes get documented here as they land. The
decisions that shape it (no TTY, stdout reserved for JSON-RPC, same use cases as the CLI) are
in [`architecture.md`](architecture.md).
