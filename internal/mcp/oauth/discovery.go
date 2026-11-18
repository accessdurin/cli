package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/mcp/jsurl"
	s "github.com/accessdurin/cli/internal/mcp/schema"
)

// LatestProtocolVersion is LATEST_PROTOCOL_VERSION, sent as MCP-Protocol-Version on discovery.
const LatestProtocolVersion = "2025-11-25"

func parseWith(schema s.Schema, v any) (any, error) { return s.Parse(schema, v) }

// ProtectedResourceDiscoveryOptions are the options of discoverOAuthProtectedResourceMetadata.
type ProtectedResourceDiscoveryOptions struct {
	ProtocolVersion     string     // default LatestProtocolVersion
	ResourceMetadataURL *jsurl.URL // explicit metadata URL (no fallback)
}

func buildWellKnownPath(prefix, pathname string) string {
	pathname = strings.TrimSuffix(pathname, "/")
	return "/.well-known/" + prefix + pathname
}

// shouldAttemptFallback: a 4xx (or 502) on a path-aware URL falls back to the root URL.
func shouldAttemptFallback(resp *http.Response, pathname string) bool {
	if resp == nil {
		return true
	}
	if pathname == "/" {
		return false
	}
	return (!httpx.OK(resp) && resp.StatusCode < 500) || resp.StatusCode == 502
}

func tryMetadataDiscovery(ctx context.Context, url, protocolVersion string, fetch httpx.Fetch) (*http.Response, error) {
	h := http.Header{}
	h.Set("MCP-Protocol-Version", protocolVersion)
	return httpx.WithinOrigin(fetch)(ctx, url, &httpx.RequestInit{Header: h})
}

func discoverMetadataWithFallback(ctx context.Context, serverURL, wellKnownType string, fetch httpx.Fetch, protocolVersion string, metadataURL *jsurl.URL) (*http.Response, error) {
	issuer, err := jsurl.Parse(serverURL, nil)
	if err != nil {
		return nil, err
	}
	if protocolVersion == "" {
		protocolVersion = LatestProtocolVersion
	}
	var url *jsurl.URL
	if metadataURL != nil {
		url = metadataURL.Clone()
	} else {
		url, err = jsurl.Parse(buildWellKnownPath(wellKnownType, issuer.Pathname()), issuer)
		if err != nil {
			return nil, err
		}
		url.SetSearch(issuer.Search())
	}
	resp, err := tryMetadataDiscovery(ctx, url.Href(), protocolVersion, fetch)
	if err != nil {
		return nil, err
	}
	if metadataURL == nil && shouldAttemptFallback(resp, issuer.Pathname()) {
		root, err := jsurl.Parse("/.well-known/"+wellKnownType, issuer)
		if err != nil {
			return nil, err
		}
		// The SDK leaves the first body unread; it is closed here to release the connection.
		httpx.Discard(resp)
		return tryMetadataDiscovery(ctx, root.Href(), protocolVersion, fetch)
	}
	return resp, nil
}

// DiscoverOAuthProtectedResourceMetadata is discoverOAuthProtectedResourceMetadata (RFC 9728):
// it tries /.well-known/oauth-protected-resource{path} (keeping the server URL's query), then,
// for a 4xx or 502 on a URL with a path, /.well-known/oauth-protected-resource at the root. A
// missing document (404) or any other non-2xx is an error.
func DiscoverOAuthProtectedResourceMetadata(ctx context.Context, serverURL string, opts *ProtectedResourceDiscoveryOptions, fetch httpx.Fetch) (*ProtectedResourceMetadata, error) {
	if opts == nil {
		opts = &ProtectedResourceDiscoveryOptions{}
	}
	resp, err := discoverMetadataWithFallback(ctx, serverURL, "oauth-protected-resource", fetch, opts.ProtocolVersion, opts.ResourceMetadataURL)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.StatusCode == 404 {
		httpx.Discard(resp)
		return nil, errors.New("Resource server does not implement OAuth 2.0 Protected Resource Metadata.")
	}
	if !httpx.OK(resp) {
		httpx.Discard(resp)
		return nil, fmt.Errorf("HTTP %d trying to load well-known OAuth protected resource metadata.", resp.StatusCode)
	}
	body, err := httpx.JSON(resp)
	if err != nil {
		return nil, err
	}
	parsed, err := parseWith(ProtectedResourceMetadataSchema, body)
	if err != nil {
		return nil, err
	}
	return newProtectedResourceMetadata(parsed.(*jsonv.Object)), nil
}

type discoveryURL struct {
	url  *jsurl.URL
	oidc bool
}

// BuildDiscoveryURLs is buildDiscoveryUrls: authorization-server metadata URLs in priority
// order. Without a path: RFC 8414 then OIDC at the root. With a path: RFC 8414 path-inserted,
// OIDC path-inserted, then OIDC path-appended.
func BuildDiscoveryURLs(authorizationServerURL string) ([]string, error) {
	urls, err := buildDiscoveryURLs(authorizationServerURL)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(urls))
	for i, u := range urls {
		out[i] = u.url.Href()
	}
	return out, nil
}

func buildDiscoveryURLs(authorizationServerURL string) ([]discoveryURL, error) {
	u, err := jsurl.Parse(authorizationServerURL, nil)
	if err != nil {
		return nil, err
	}
	origin, err := jsurl.Parse(u.Origin(), nil)
	if err != nil {
		return nil, err
	}
	mk := func(path string, oidc bool) discoveryURL {
		x, err2 := jsurl.Parse(path, origin)
		if err2 != nil {
			err = err2
		}
		return discoveryURL{x, oidc}
	}
	var out []discoveryURL
	if u.Pathname() == "/" {
		out = append(out, mk("/.well-known/oauth-authorization-server", false), mk("/.well-known/openid-configuration", true))
	} else {
		pathname := strings.TrimSuffix(u.Pathname(), "/")
		out = append(out,
			mk("/.well-known/oauth-authorization-server"+pathname, false),
			mk("/.well-known/openid-configuration"+pathname, true),
			mk(pathname+"/.well-known/openid-configuration", true))
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AuthorizationServerDiscoveryOptions are the options of discoverAuthorizationServerMetadata.
type AuthorizationServerDiscoveryOptions struct {
	Fetch                httpx.Fetch
	ProtocolVersion      string // default LatestProtocolVersion
	SkipIssuerValidation bool
}

// DiscoverAuthorizationServerMetadata is discoverAuthorizationServerMetadata (RFC 8414 / OpenID
// Connect Discovery). It tries BuildDiscoveryURLs in order with headers MCP-Protocol-Version and
// Accept: application/json; a 4xx or 502 moves on to the next URL, any other non-2xx is an
// error. It returns (nil, nil) when every URL was skipped. The metadata's issuer must equal
// authorizationServerURL (tolerating one trailing slash on the latter) unless
// SkipIssuerValidation is set; otherwise an *IssuerMismatchError is returned.
func DiscoverAuthorizationServerMetadata(ctx context.Context, authorizationServerURL string, opts AuthorizationServerDiscoveryOptions) (*AuthorizationServerMetadata, error) {
	pv := opts.ProtocolVersion
	if pv == "" {
		pv = LatestProtocolVersion
	}
	urls, err := buildDiscoveryURLs(authorizationServerURL)
	if err != nil {
		return nil, err
	}
	for _, d := range urls {
		h := http.Header{}
		h.Set("MCP-Protocol-Version", pv)
		h.Set("Accept", "application/json")
		resp, err := httpx.WithinOrigin(opts.Fetch)(ctx, d.url.Href(), &httpx.RequestInit{Header: h})
		if err != nil {
			return nil, err
		}
		if !httpx.OK(resp) {
			httpx.Discard(resp)
			if resp.StatusCode < 500 || resp.StatusCode == 502 {
				continue
			}
			kind := "OAuth"
			if d.oidc {
				kind = "OpenID provider"
			}
			return nil, fmt.Errorf("HTTP %d trying to load %s metadata from %s", resp.StatusCode, kind, d.url.Href())
		}
		body, err := httpx.JSON(resp)
		if err != nil {
			return nil, err
		}
		schema := s.Schema(AuthorizationServerMetadataSchema)
		if d.oidc {
			schema = OpenIDProviderDiscoveryMetadataSchema
		}
		parsed, err := parseWith(schema, body)
		if err != nil {
			return nil, err
		}
		meta := newAuthorizationServerMetadata(parsed.(*jsonv.Object))
		if !opts.SkipIssuerValidation {
			expected := authorizationServerURL
			if !(meta.Issuer == expected || (strings.HasSuffix(expected, "/") && meta.Issuer == expected[:len(expected)-1])) {
				received := meta.Issuer
				return nil, &IssuerMismatchError{Kind: "metadata", Expected: expected, Received: &received}
			}
		}
		return meta, nil
	}
	return nil, nil
}

// ServerInfo is the result of DiscoverOAuthServerInfo.
type ServerInfo struct {
	AuthorizationServerURL      string
	AuthorizationServerMetadata *AuthorizationServerMetadata // nil when not found
	ResourceMetadata            *ProtectedResourceMetadata   // nil when not found
}

// ServerInfoOptions are the options of discoverOAuthServerInfo.
type ServerInfoOptions struct {
	ResourceMetadataURL          *jsurl.URL
	Fetch                        httpx.Fetch
	SkipIssuerMetadataValidation bool
}

// DiscoverOAuthServerInfo is discoverOAuthServerInfo: protected-resource metadata names the
// authorization server (its first entry); any discovery failure other than a TypeError (network
// failure, invalid URL) falls back to the server's origin as the authorization server.
func DiscoverOAuthServerInfo(ctx context.Context, serverURL string, opts ServerInfoOptions) (*ServerInfo, error) {
	info := &ServerInfo{}
	rm, err := DiscoverOAuthProtectedResourceMetadata(ctx, serverURL, &ProtectedResourceDiscoveryOptions{ResourceMetadataURL: opts.ResourceMetadataURL}, opts.Fetch)
	if err != nil {
		if httpx.IsTypeError(err) {
			return nil, err
		}
	} else {
		info.ResourceMetadata = rm
		if len(rm.AuthorizationServers) > 0 {
			info.AuthorizationServerURL = rm.AuthorizationServers[0]
		}
	}
	if info.AuthorizationServerURL == "" {
		root, err := jsurl.ParseRef("/", serverURL)
		if err != nil {
			return nil, err
		}
		info.AuthorizationServerURL = root.Href()
	}
	meta, err := DiscoverAuthorizationServerMetadata(ctx, info.AuthorizationServerURL, AuthorizationServerDiscoveryOptions{Fetch: opts.Fetch, SkipIssuerValidation: opts.SkipIssuerMetadataValidation})
	if err != nil {
		return nil, err
	}
	info.AuthorizationServerMetadata = meta
	return info, nil
}
