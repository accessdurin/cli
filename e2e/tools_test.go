package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func sampleTools() []Tool {
	return []Tool{
		{
			"name": "github.create_issue", "title": "Create issue", "description": "Opens an issue\u202e with bidi",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}, "required": []string{"title"}},
			"annotations": map[string]any{"destructiveHint": true},
			"x-extra":     map[string]any{"z": 1, "a": []any{1.5, 1e21, "é"}},
		},
		{"name": "bad name; rm -rf", "inputSchema": map[string]any{"type": "object"}},
		{"name": "linear:list/issues", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}, "outputSchema": map[string]any{"type": "object"}},
	}
}

// signedIn returns an environment with one active profile and a seeded grant.
func signedIn(t *testing.T) (*Env, string) {
	e := NewEnv(t)
	requireKeyring(t)
	id := e.SaveProfile(e.DefaultProfile())
	e.WriteFile(filepath.Join(e.Config, "active"), id+"\n", 0o600)
	e.SeedGrant(id, "access-seeded", "refresh-seeded")
	return e, id
}

func TestToolsJSON(t *testing.T) {
	e, id := signedIn(t)
	e.Server.Pages = [][]Tool{sampleTools()}
	e.Run("", "tools", "list", "--config-dir", e.Config)
	e.NoteRequests()
	e.Run("", "tools", "inspect", "github.create_issue", "--profile", id, "--config-dir", e.Config)
	e.Run("", "tools", "inspect", "bad name; rm -rf", "--profile", id, "--config-dir", e.Config)
	e.Run("", "tools", "inspect", "missing", "--profile", id, "--config-dir", e.Config)
	e.Run("", "verify", "--config-dir", e.Config)
	e.Run("", "mcp", "endpoint", "--config-dir", e.Config)
	e.Server.ResetLog()

	input := filepath.Join(e.Root, "input.json")
	e.WriteFile(input, `{"title":"Bug","labels":["a"],"n":1.0}`, 0o600)
	e.Server.CallReplies["github.create_issue"] = []CallReply{
		{Result: map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": "created \u001b[31m", "extra": 1}},
			"structuredContent": map[string]any{"url": "https://example.com/1"},
			"unknownTop":        true,
			"_meta":             map[string]any{"io.durin/control": map[string]any{"idempotencyKey": "op-1"}},
		}},
		{Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": "denied"}}, "isError": true}},
		{Result: map[string]any{"structuredContent": map[string]any{"status": "approval_required", "approvalId": "appr_1"}}},
		{Error: map[string]any{"code": -32602, "message": "Invalid params \u001b]8;;x"}},
	}
	e.Run("", "tools", "call", "github.create_issue", "--input", input, "--idempotency-key", "op-1", "--config-dir", e.Config)
	e.NoteRequests()
	e.Run(`{"title":"Again"}`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "op-2", "--approval-id", "appr_0", "--config-dir", e.Config)
	e.NoteRequests()
	e.Run(`{}`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "op-3", "--config-dir", e.Config)
	e.Run(`{}`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "op-4", "--config-dir", e.Config)
	e.Server.ResetLog()

	// Invalid calls are refused before anything is sent.
	e.Run(`{}`, "tools", "call", "github.create_issue", "--input", "-", "--config-dir", e.Config)
	e.Run(`{}`, "tools", "call", "github.create_issue", "--idempotency-key", "k", "--config-dir", e.Config)
	e.Run(`[1]`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "k", "--config-dir", e.Config)
	e.Run(`{"a":`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "k", "--config-dir", e.Config)
	e.Run(`{}`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "bad key!", "--config-dir", e.Config)
	e.Run(`{}`, "tools", "call", "bad;name", "--input", "-", "--idempotency-key", "k", "--config-dir", e.Config)
	e.Run(`{}`, "tools", "call", "github.create_issue", "--input", filepath.Join(e.Root, "missing.json"), "--idempotency-key", "k", "--config-dir", e.Config)
	e.Run(`{"big":"`+strings.Repeat("x", 1_048_576)+`"}`, "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "k", "--config-dir", e.Config)
	e.Run("\ufeff{}", "tools", "call", "github.create_issue", "--input", "-", "--idempotency-key", "k", "--config-dir", e.Config)
	e.NoteRequests()
}

func TestToolsSSEAndPagination(t *testing.T) {
	e, id := signedIn(t)
	e.Server.SSE = true
	var pages [][]Tool
	for p := 0; p < 3; p++ {
		pages = append(pages, []Tool{{"name": fmt.Sprintf("tool_%d", p), "inputSchema": map[string]any{"type": "object"}}})
	}
	e.Server.Pages = pages
	e.Run("", "tools", "list", "--profile", id, "--config-dir", e.Config)
	e.NoteRequests()
	e.Run(`{"q":1}`, "tools", "call", "tool_1", "--input", "-", "--idempotency-key", "op-1", "--profile", id, "--config-dir", e.Config)
	e.NoteRequests()

	// Duplicate tool names make the catalog invalid.
	e.Server.Pages = [][]Tool{{{"name": "dup", "inputSchema": map[string]any{"type": "object"}}}, {{"name": "dup", "inputSchema": map[string]any{"type": "object"}}}}
	e.Run("", "tools", "list", "--profile", id, "--config-dir", e.Config)
	// A tool whose inputSchema is not an object schema makes the catalog invalid.
	e.Server.Pages = [][]Tool{{{"name": "weird", "inputSchema": map[string]any{"type": "string"}}}}
	e.Run("", "tools", "list", "--profile", id, "--config-dir", e.Config)
	// More than 64 pages.
	pages = nil
	for p := 0; p < 70; p++ {
		pages = append(pages, []Tool{{"name": fmt.Sprintf("t%d", p), "inputSchema": map[string]any{"type": "object"}}})
	}
	e.Server.Pages = pages
	e.Run("", "tools", "list", "--profile", id, "--config-dir", e.Config)
	e.Server.ResetLog()
}

func TestAuthorizationFailures(t *testing.T) {
	e := NewEnv(t)
	requireKeyring(t)
	// No profile at all.
	e.Run("", "tools", "list", "--config-dir", e.Config)
	e.Run("", "verify", "--config-dir", e.Config)
	e.Run("", "mcp", "serve", "--config-dir", e.Config)

	id := e.SaveProfile(e.DefaultProfile())
	e.WriteFile(filepath.Join(e.Config, "active"), id+"\n", 0o600)
	// A profile that never signed in.
	e.Run("", "tools", "list", "--config-dir", e.Config)
	e.NoteRequests()

	// An expired access token is refreshed with the stored refresh token and the new tokens saved.
	e.SeedGrant(id, "access-expired", "refresh-seeded")
	delete(e.Server.AcceptedTokens, "access-expired")
	e.Server.Pages = [][]Tool{{{"name": "a", "inputSchema": map[string]any{"type": "object"}}}}
	e.Run("", "tools", "list", "--config-dir", e.Config)
	e.NoteRequests()
	tokens, _ := e.Credential("mcp:" + id + ":tokens")
	e.Note("stored tokens after refresh", tokens)

	// A refused refresh ends the sign-in.
	e.SeedGrant(id, "access-expired", "refresh-revoked")
	delete(e.Server.AcceptedTokens, "access-expired")
	e.Server.RefreshStatus = 400
	e.Run("", "tools", "list", "--config-dir", e.Config)
	e.NoteRequests()
	e.Note("credentials after refused refresh", e.CredentialKeys("mcp:"+id))

	// No refresh token at all.
	e.SeedGrant(id, "access-expired", "")
	delete(e.Server.AcceptedTokens, "access-expired")
	e.Run("", "verify", "--config-dir", e.Config)
	e.NoteRequests()

	// Saved credentials that do not parse, or name another issuer.
	e.StoreCredential("mcp:"+id+":tokens", "not json")
	e.Run("", "verify", "--config-dir", e.Config)
	e.StoreCredential("mcp:"+id+":tokens", `{"access_token":"access-seeded","token_type":"Bearer","issuer":"https://other.example"}`)
	e.Run("", "verify", "--config-dir", e.Config)
	e.NoteRequests()

	// A server error during a call is reported without detail.
	e.SeedGrant(id, "access-seeded", "refresh-seeded")
	e.Server.MCPHook = func(method string, _ []byte) bool { return method == "tools/call" }
	e.Run(`{}`, "tools", "call", "a", "--input", "-", "--idempotency-key", "k", "--config-dir", e.Config)
	e.NoteRequests()
}
