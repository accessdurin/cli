// Package mcp is a Go port of the subset of the TypeScript MCP SDK
// (@modelcontextprotocol/client, core and server 2.3.1) that the durin CLI uses.
//
// Layout:
//   - mcp (this package): JSON-RPC message parsing and predicates, the Streamable HTTP client
//     transport, the Client (initialize, listTools, callTool), the stdio server transport (bridge)
//     and stdio client transport (setup verification).
//   - mcp/oauth: auth() and its helpers (RFC 9728 / RFC 8414 / OIDC discovery, dynamic client
//     registration, PKCE, code exchange, refresh, RFC 9207 iss checks, SEP-2352 issuer stamps).
//   - mcp/httpx: the injected fetch abstraction (no default HTTP client is ever used).
//   - mcp/schema: the zod 4.6.5 parse semantics the SDK's schemas rely on, over jsjson values.
//
// JSON values are internal/jsjson values (a nil jsjson.Value is undefined) and URLs are
// internal/weburl URLs, the module-wide JavaScript-compatible models.
//
// Faithfulness is checked against the real SDK: testdata/record.mjs and testdata/stdio.mjs run
// the TypeScript SDK on the scenarios in testdata/scenarios and record request sequences,
// provider calls, warnings, results and stdio framing into testdata/fixtures, which the Go tests
// replay without Node.
//
// Only the SDK's default 'legacy' version negotiation (initialize with 2025-11-25) is ported;
// server/discover negotiation, the 2026-07-28 wire era, input_required, DPoP, SEP-2243 header
// mirroring and the SSE (pre-Streamable) transport are not.
package mcp
