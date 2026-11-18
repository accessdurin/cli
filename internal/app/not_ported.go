package app

import (
	"context"
	"net/http"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
)

// TODO(go-port): setup stops here until setup-mcp.ts is ported, before it saves a profile or
// touches the agent's configuration.
var errNotPorted = clierror.New("This command is not yet available in the Go build of durin.")

// mcpIssuer is mcpIssuer (src/setup-mcp.ts): the personal endpoint's one trusted
// authorization server, from its protected resource metadata.
func mcpIssuer(ctx context.Context, resource string, request *http.Client, scope contracts.CliHostScope) (string, error) {
	return "", errNotPorted // TODO(go-port): discoverOAuthProtectedResourceMetadata and host checks.
}

// authorizeMcp is authorizeMcp (src/setup-mcp.ts), run under the profile lock.
func authorizeMcp(ctx context.Context, options authorizeMcpOptions) (int, error) {
	return 0, errNotPorted // TODO(go-port): McpOAuthProvider, the loopback callback and RemoteMcpClient.
}
