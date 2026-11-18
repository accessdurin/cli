package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
	s "github.com/accessdurin/cli/internal/mcp/schema"
	"github.com/accessdurin/cli/internal/weburl"
)

// Tokens is OAuthTokens / StoredOAuthTokens. Field order is the schema's declaration order, which
// is the order zod emits, so json.Marshal produces the same bytes as JSON.stringify of a parsed
// value. Optional fields are nil when absent.
type Tokens struct {
	AccessToken  string   `json:"access_token"`
	IDToken      *string  `json:"id_token,omitempty"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    *float64 `json:"expires_in,omitempty"`
	Scope        *string  `json:"scope,omitempty"`
	RefreshToken *string  `json:"refresh_token,omitempty"`
	Issuer       *string  `json:"issuer,omitempty"`
}

// ParseTokens applies OAuthTokensSchema: unknown keys are stripped, expires_in is coerced with
// Number(), and a non-string issuer is dropped. It accepts every stored entry the SDK accepts.
func ParseTokens(v jsjson.Value) (*Tokens, error) {
	out, err := s.Parse(TokensSchema, v)
	if err != nil {
		return nil, err
	}
	o := out.(*jsjson.Object)
	t := &Tokens{AccessToken: str(o, "access_token"), TokenType: str(o, "token_type")}
	t.IDToken = optStr(o, "id_token")
	t.Scope = optStr(o, "scope")
	t.RefreshToken = optStr(o, "refresh_token")
	t.Issuer = optStr(o, "issuer")
	if n, ok := o.GetNumber("expires_in"); ok {
		t.ExpiresIn = &n
	}
	return t, nil
}

// ParseTokensJSON parses JSON text and applies ParseTokens.
func ParseTokensJSON(data []byte) (*Tokens, error) {
	v, err := jsjson.Parse(data)
	if err != nil {
		return nil, err
	}
	return ParseTokens(v)
}

// UnmarshalJSON applies OAuthTokensSchema, so json.Unmarshal accepts exactly what the SDK does.
func (t *Tokens) UnmarshalJSON(data []byte) error {
	parsed, err := ParseTokensJSON(data)
	if err != nil {
		return err
	}
	*t = *parsed
	return nil
}

// Object is JSON.stringify's view of the tokens: the keys present, in schema order.
func (t Tokens) Object() *jsjson.Object {
	o := jsjson.Obj(jsjson.KV("access_token", jsjson.String(t.AccessToken)))
	opt := func(key string, v *string) {
		if v != nil {
			o.Set(key, jsjson.String(*v))
		}
	}
	opt("id_token", t.IDToken)
	o.Set("token_type", jsjson.String(t.TokenType))
	if t.ExpiresIn != nil {
		o.Set("expires_in", jsjson.Number(*t.ExpiresIn))
	}
	opt("scope", t.Scope)
	opt("refresh_token", t.RefreshToken)
	opt("issuer", t.Issuer)
	return o
}

// MarshalJSON writes the tokens as JSON.stringify writes the parsed object.
func (t Tokens) MarshalJSON() ([]byte, error) { return []byte(jsjson.Stringify(t.Object())), nil }

// withIssuer returns a copy carrying the issuer stamp.
func (t Tokens) withIssuer(issuer string) Tokens {
	t.Issuer = &issuer
	return t
}

// ClientInformation is StoredOAuthClientInformation: OAuth client information optionally merged
// with registered client metadata (OAuthClientInformationFull) and the SEP-2352 issuer stamp.
// Field order matches OAuthClientInformationFullSchema. Optional fields are nil when absent.
type ClientInformation struct {
	RedirectURIs            []string        `json:"redirect_uris,omitempty"`
	TokenEndpointAuthMethod *string         `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string        `json:"grant_types,omitempty"`
	ResponseTypes           []string        `json:"response_types,omitempty"`
	ApplicationType         *string         `json:"application_type,omitempty"`
	ClientName              *string         `json:"client_name,omitempty"`
	ClientURI               *string         `json:"client_uri,omitempty"`
	LogoURI                 *string         `json:"logo_uri,omitempty"`
	Scope                   *string         `json:"scope,omitempty"`
	Contacts                []string        `json:"contacts,omitempty"`
	TosURI                  *string         `json:"tos_uri,omitempty"`
	PolicyURI               *string         `json:"policy_uri,omitempty"`
	JwksURI                 *string         `json:"jwks_uri,omitempty"`
	Jwks                    json.RawMessage `json:"jwks,omitempty"`
	SoftwareID              *string         `json:"software_id,omitempty"`
	SoftwareVersion         *string         `json:"software_version,omitempty"`
	SoftwareStatement       *string         `json:"software_statement,omitempty"`
	ClientID                string          `json:"client_id"`
	ClientSecret            *string         `json:"client_secret,omitempty"`
	ClientIDIssuedAt        *float64        `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   *float64        `json:"client_secret_expires_at,omitempty"`
	Issuer                  *string         `json:"issuer,omitempty"`

	// present records which array fields were present (an empty array is still present).
	present map[string]bool
}

// MarshalJSON keeps present-but-empty arrays, which zod would keep.
func (c ClientInformation) MarshalJSON() ([]byte, error) {
	o, err := c.Object()
	if err != nil {
		return nil, err
	}
	return []byte(jsjson.Stringify(o)), nil
}

// Object is JSON.stringify's view of the client information: the keys present, in schema
// order (present-but-empty arrays kept, as zod keeps them).
func (c ClientInformation) Object() (*jsjson.Object, error) {
	o := jsjson.NewObject()
	arr := func(key string, v []string) {
		if v != nil || c.present[key] {
			o.Set(key, jsjson.Strings(v...))
		}
	}
	opt := func(key string, v *string) {
		if v != nil {
			o.Set(key, jsjson.String(*v))
		}
	}
	num := func(key string, v *float64) {
		if v != nil {
			o.Set(key, jsjson.Number(*v))
		}
	}
	arr("redirect_uris", c.RedirectURIs)
	opt("token_endpoint_auth_method", c.TokenEndpointAuthMethod)
	arr("grant_types", c.GrantTypes)
	arr("response_types", c.ResponseTypes)
	opt("application_type", c.ApplicationType)
	opt("client_name", c.ClientName)
	opt("client_uri", c.ClientURI)
	opt("logo_uri", c.LogoURI)
	opt("scope", c.Scope)
	arr("contacts", c.Contacts)
	opt("tos_uri", c.TosURI)
	opt("policy_uri", c.PolicyURI)
	opt("jwks_uri", c.JwksURI)
	if c.Jwks != nil {
		v, err := jsjson.Parse(c.Jwks)
		if err != nil {
			return nil, err
		}
		o.Set("jwks", v)
	}
	opt("software_id", c.SoftwareID)
	opt("software_version", c.SoftwareVersion)
	opt("software_statement", c.SoftwareStatement)
	o.Set("client_id", jsjson.String(c.ClientID))
	opt("client_secret", c.ClientSecret)
	num("client_id_issued_at", c.ClientIDIssuedAt)
	num("client_secret_expires_at", c.ClientSecretExpiresAt)
	opt("issuer", c.Issuer)
	return o, nil
}

// ClientInformationFromObject is the typed view of an already parsed client information
// object (for providers that parse stored credentials themselves).
func ClientInformationFromObject(o *jsjson.Object) *ClientInformation { return clientInfoFromObject(o) }

func clientInfoFromObject(o *jsjson.Object) *ClientInformation {
	c := &ClientInformation{present: map[string]bool{}}
	arr := func(key string) []string {
		v, ok := o.Get(key)
		if !ok {
			return nil
		}
		a, ok := v.(jsjson.Array)
		if !ok {
			return nil
		}
		c.present[key] = true
		out := make([]string, len(a))
		for i, x := range a {
			sx, _ := x.(jsjson.String)
			out[i] = string(sx)
		}
		return out
	}
	num := func(key string) *float64 {
		if f, ok := o.GetNumber(key); ok {
			return &f
		}
		return nil
	}
	c.RedirectURIs = arr("redirect_uris")
	c.TokenEndpointAuthMethod = optStr(o, "token_endpoint_auth_method")
	c.GrantTypes = arr("grant_types")
	c.ResponseTypes = arr("response_types")
	c.ApplicationType = optStr(o, "application_type")
	c.ClientName = optStr(o, "client_name")
	c.ClientURI = optStr(o, "client_uri")
	c.LogoURI = optStr(o, "logo_uri")
	c.Scope = optStr(o, "scope")
	c.Contacts = arr("contacts")
	c.TosURI = optStr(o, "tos_uri")
	c.PolicyURI = optStr(o, "policy_uri")
	c.JwksURI = optStr(o, "jwks_uri")
	if v, ok := o.Get("jwks"); ok && v != nil {
		c.Jwks = json.RawMessage(jsjson.Stringify(v))
	}
	c.SoftwareID = optStr(o, "software_id")
	c.SoftwareVersion = optStr(o, "software_version")
	c.SoftwareStatement = optStr(o, "software_statement")
	c.ClientID = str(o, "client_id")
	c.ClientSecret = optStr(o, "client_secret")
	c.ClientIDIssuedAt = num("client_id_issued_at")
	c.ClientSecretExpiresAt = num("client_secret_expires_at")
	c.Issuer = optStr(o, "issuer")
	return c
}

// ParseClientInformation applies OAuthClientInformationSchema (client_id, client_secret,
// client_id_issued_at, client_secret_expires_at, issuer; everything else stripped).
func ParseClientInformation(v jsjson.Value) (*ClientInformation, error) {
	out, err := s.Parse(ClientInformationSchema, v)
	if err != nil {
		return nil, err
	}
	return clientInfoFromObject(out.(*jsjson.Object)), nil
}

// ParseClientInformationFull applies OAuthClientInformationFullSchema (registration responses).
func ParseClientInformationFull(v jsjson.Value) (*ClientInformation, error) {
	out, err := s.Parse(ClientInformationFullSchema, v)
	if err != nil {
		return nil, err
	}
	return clientInfoFromObject(out.(*jsjson.Object)), nil
}

// ParseClientMetadata applies OAuthClientMetadataSchema and returns the parsed object.
func ParseClientMetadata(v jsjson.Value) (*jsjson.Object, error) {
	out, err := s.Parse(ClientMetadataSchema, v)
	if err != nil {
		return nil, err
	}
	return out.(*jsjson.Object), nil
}

func (c ClientInformation) withIssuer(issuer string) ClientInformation {
	c.Issuer = &issuer
	return c
}

func str(o *jsjson.Object, key string) string {
	sv, _ := o.GetString(key)
	return sv
}

func optStr(o *jsjson.Object, key string) *string {
	sv, ok := o.GetString(key)
	if !ok {
		return nil
	}
	return &sv
}

func optBool(o *jsjson.Object, key string) *bool {
	b, ok := o.GetBool(key)
	if !ok {
		return nil
	}
	return &b
}

func strs(o *jsjson.Object, key string) []string {
	a, ok := o.GetArray(key)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if sv, ok := x.(jsjson.String); ok {
			out = append(out, string(sv))
		}
	}
	return out
}

// ProtectedResourceMetadata is parsed RFC 9728 metadata. Raw is the zod-parsed object (loose:
// undeclared keys kept); the typed fields are views of it.
type ProtectedResourceMetadata struct {
	Raw                  *jsjson.Object
	Resource             string
	AuthorizationServers []string // nil when absent
	ScopesSupported      []string // nil when absent
}

func newProtectedResourceMetadata(o *jsjson.Object) *ProtectedResourceMetadata {
	return &ProtectedResourceMetadata{Raw: o, Resource: str(o, "resource"), AuthorizationServers: strs(o, "authorization_servers"), ScopesSupported: strs(o, "scopes_supported")}
}

// AuthorizationServerMetadata is parsed RFC 8414 or OpenID Connect discovery metadata. Raw is the
// zod-parsed object; the typed fields are views of it.
type AuthorizationServerMetadata struct {
	Raw                                        *jsjson.Object
	Issuer                                     string
	AuthorizationEndpoint                      string
	TokenEndpoint                              string
	RegistrationEndpoint                       *string
	RevocationEndpoint                         *string
	ScopesSupported                            []string
	ResponseTypesSupported                     []string
	CodeChallengeMethodsSupported              []string // nil when absent
	TokenEndpointAuthMethodsSupported          []string // nil when absent
	ClientIDMetadataDocumentSupported          *bool
	AuthorizationResponseIssParameterSupported *bool
}

func newAuthorizationServerMetadata(o *jsjson.Object) *AuthorizationServerMetadata {
	return &AuthorizationServerMetadata{
		Raw: o, Issuer: str(o, "issuer"), AuthorizationEndpoint: str(o, "authorization_endpoint"),
		TokenEndpoint: str(o, "token_endpoint"), RegistrationEndpoint: optStr(o, "registration_endpoint"),
		RevocationEndpoint: optStr(o, "revocation_endpoint"), ScopesSupported: strs(o, "scopes_supported"),
		ResponseTypesSupported:                     strs(o, "response_types_supported"),
		CodeChallengeMethodsSupported:              strs(o, "code_challenge_methods_supported"),
		TokenEndpointAuthMethodsSupported:          strs(o, "token_endpoint_auth_methods_supported"),
		ClientIDMetadataDocumentSupported:          optBool(o, "client_id_metadata_document_supported"),
		AuthorizationResponseIssParameterSupported: optBool(o, "authorization_response_iss_parameter_supported"),
	}
}

// DiscoveryState is OAuthDiscoveryState, saved by auth() so later calls (and the callback leg)
// can skip discovery and bind the code exchange to the authorization server that minted it.
type DiscoveryState struct {
	AuthorizationServerURL      string
	ResourceMetadataURL         *string
	ResourceMetadata            *ProtectedResourceMetadata
	AuthorizationServerMetadata *AuthorizationServerMetadata
}

// ClientInformationContext is OAuthClientInformationContext: the authorization server a stored
// credential is read for or written for.
type ClientInformationContext struct {
	Issuer string
}

// InvalidationScope is the argument of invalidateCredentials.
type InvalidationScope string

// Invalidation scopes.
const (
	InvalidateAll       InvalidationScope = "all"
	InvalidateClient    InvalidationScope = "client"
	InvalidateTokens    InvalidationScope = "tokens"
	InvalidateVerifier  InvalidationScope = "verifier"
	InvalidateDiscovery InvalidationScope = "discovery"
)

// ClientProvider mirrors the OAuthClientProvider methods the CLI's McpOAuthProvider implements.
//
// In TypeScript several of these are optional; this port treats them all as implemented (as
// the CLI does). Consequences relative to the SDK's "method absent" branches:
//   - State returning "" means no state parameter (TS: state() absent or falsy);
//   - RedirectURL returning "" selects the non-interactive flow (TS: redirectUrl undefined);
//   - saveClientInformation/saveDiscoveryState/validateResourceURL/invalidateCredentials are
//     always present, so the SDK branches for providers lacking them are not ported.
//
// Optional SDK hooks the CLI does not implement (addClientAuthentication, prepareTokenRequest,
// dpop, clientMetadataUrl, saveAuthorizationServerUrl, saveResourceUrl) are treated as absent.
//
// A nil *ClientInformationContext corresponds to calling the TS method without a context.
type ClientProvider interface {
	RedirectURL() string
	// ClientMetadata returns the RFC 7591 metadata to register, as an ordered object (its key
	// order is the registration request body's key order).
	ClientMetadata() *jsjson.Object
	State(ctx context.Context) (string, error)
	RedirectToAuthorization(ctx context.Context, authorizationURL *weburl.URL) error
	SaveCodeVerifier(ctx context.Context, verifier string) error
	CodeVerifier(ctx context.Context) (string, error)
	ClientInformation(ctx context.Context, info *ClientInformationContext) (*ClientInformation, error)
	SaveClientInformation(ctx context.Context, client ClientInformation, info *ClientInformationContext) error
	Tokens(ctx context.Context, info *ClientInformationContext) (*Tokens, error)
	SaveTokens(ctx context.Context, tokens Tokens, info *ClientInformationContext) error
	// ValidateResourceURL receives the server URL (fragment removed) and the metadata's
	// resource (nil when there is no protected-resource metadata) and returns the RFC 8707
	// resource indicator to use, or nil for none.
	ValidateResourceURL(ctx context.Context, serverURL *weburl.URL, resource *string) (*weburl.URL, error)
	SaveDiscoveryState(ctx context.Context, state DiscoveryState) error
	DiscoveryState(ctx context.Context) (*DiscoveryState, error)
	InvalidateCredentials(ctx context.Context, scope InvalidationScope) error
}

// describe is String(v) for a JSON value.
func describe(v jsjson.Value) string {
	switch t := v.(type) {
	case nil:
		return "undefined"
	case jsjson.Null:
		return "null"
	case jsjson.Bool:
		return fmt.Sprint(bool(t))
	case jsjson.Number:
		return jsjson.FormatNumber(float64(t))
	case jsjson.String:
		return string(t)
	case jsjson.Array:
		parts := make([]string, len(t))
		for i, x := range t {
			if _, null := x.(jsjson.Null); x != nil && !null {
				parts[i] = describe(x)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}
