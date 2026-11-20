package app

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp"
	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

const (
	// mcpConnectTimeout bounds the initialize handshake and each tools/list page.
	mcpConnectTimeout = 15 * time.Second
	// mcpCallTimeout bounds one tool call.
	mcpCallTimeout = 60 * time.Second
	// mcpListMaxPages caps the tools/list pages one listing walks.
	mcpListMaxPages = 64
	// controlKey is the _meta key that carries the call's idempotency key and approval.
	controlKey = "io.durin/control"
)

// RemoteMcpOptions configure a RemoteMcpClient. Fetch must already keep every request on the
// profile's hosts (HTTPFetch over SecureFetch). Warn receives the SDK's warnings; nil writes
// them to stderr through the sanitized console.
type RemoteMcpOptions struct {
	Resource      string
	Authorization oauth.ClientProvider
	Fetch         httpx.Fetch
	Development   bool
	Warn          func(string)
}

// RemoteMcpClient is RemoteMcpClient (src/remote-mcp.ts): an authenticated MCP session with
// the organization's gateway over Streamable HTTP.
type RemoteMcpClient struct {
	client    *mcp.Client
	transport *mcp.StreamableHTTPClientTransport
}

var _ ports.CliMcpAccess = (*RemoteMcpClient)(nil)

// newMcpTransport is the Streamable HTTP transport every CLI session uses: redirects are
// refused and an insufficient scope fails instead of starting a new sign-in.
func newMcpTransport(resource string, development bool, authorization oauth.ClientProvider, fetch httpx.Fetch, warn func(string)) (*mcp.StreamableHTTPClientTransport, error) {
	if !contracts.SafeCliURL(resource, development) {
		return nil, errors.New("Unsafe MCP resource URL.")
	}
	url, err := weburl.Parse(resource)
	if err != nil {
		return nil, err
	}
	if warn == nil {
		warn = ConsoleWarn(os.Stderr)
	}
	return mcp.NewStreamableHTTPClientTransport(url, mcp.StreamableHTTPOptions{
		OAuthProvider:       authorization,
		Fetch:               fetch,
		RequestInit:         &httpx.RequestInit{Redirect: httpx.RedirectError},
		OnInsufficientScope: "throw",
		Warn:                warn,
	})
}

// NewRemoteMcpClient fails for a resource that is not a safe CLI URL.
func NewRemoteMcpClient(options RemoteMcpOptions) (*RemoteMcpClient, error) {
	transport, err := newMcpTransport(options.Resource, options.Development, options.Authorization, options.Fetch, options.Warn)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(mcp.Implementation{Name: "Durin CLI", Version: "1.0.0"}, mcp.ClientOptions{ListMaxPages: mcpListMaxPages})
	return &RemoteMcpClient{client: client, transport: transport}, nil
}

// Connect runs the initialize handshake.
func (c *RemoteMcpClient) Connect(ctx context.Context) error {
	return c.client.Connect(ctx, c.transport, mcp.RequestOptions{Timeout: mcpConnectTimeout})
}

// Close ends the session.
func (c *RemoteMcpClient) Close() error { return c.client.Close() }

// ListTools returns the whole catalog. Tools whose names are not shell-inert are left out;
// the rest must form a valid catalog.
func (c *RemoteMcpClient) ListTools(ctx context.Context) (contracts.CliToolCatalog, error) {
	listed, err := c.client.ListTools(ctx, nil, mcp.RequestOptions{Timeout: mcpConnectTimeout})
	if err != nil {
		return nil, err
	}
	tools, _ := listed.GetArray("tools")
	kept := jsjson.Array{}
	for _, tool := range tools {
		name, _ := tool.(*jsjson.Object).Get("name")
		if zod.Valid(contracts.CliToolNameSchema, name) {
			kept = append(kept, tool)
		}
	}
	catalog, err := zod.Parse(contracts.McpToolsListSchema, listed.Clone().Set("tools", kept))
	if err != nil {
		return nil, err
	}
	return catalog.(*jsjson.Object), nil
}

// CallTool calls one tool with its idempotency key (and approval, when given) in
// _meta["io.durin/control"].
func (c *RemoteMcpClient) CallTool(ctx context.Context, input contracts.CliToolCall) (contracts.McpToolResult, error) {
	var arguments jsjson.Value
	if input.Arguments != nil {
		arguments = input.Arguments
	}
	call, ok := contracts.ParseCliToolCall(&input.Name, arguments, &input.IdempotencyKey, input.ApprovalID)
	if !ok {
		return nil, errors.New("invalid tool call")
	}
	control := jsjson.Obj(jsjson.KV("idempotencyKey", jsjson.String(call.IdempotencyKey)))
	if call.ApprovalID != "" {
		control.Set("approvalId", jsjson.String(call.ApprovalID))
	}
	result, err := c.client.CallTool(ctx, mcp.CallToolParams{
		Name:      call.Name,
		Arguments: call.Arguments,
		Meta:      jsjson.Obj(jsjson.KV(controlKey, control)),
	}, mcp.RequestOptions{Timeout: mcpCallTimeout})
	if err != nil {
		return nil, err
	}
	parsed, err := zod.Parse(contracts.McpToolResultSchema, result)
	if err != nil {
		return nil, err
	}
	return parsed.(*jsjson.Object), nil
}
