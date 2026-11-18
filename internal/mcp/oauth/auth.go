package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/mcp/jsurl"
)

// Result is AuthResult.
type Result string

// Auth results.
const (
	Authorized Result = "AUTHORIZED"
	Redirect   Result = "REDIRECT"
)

// Options are the options of auth().
type Options struct {
	ServerURL                    string
	AuthorizationCode            *string
	Iss                          *string
	Scope                        string // "" means none
	ResourceMetadataURL          *jsurl.URL
	Fetch                        httpx.Fetch
	SkipIssuerMetadataValidation bool
	ForceReauthorization         bool
	// Warn receives the SDK's console.warn messages (for example
	// `[mcp-sdk] OAuth "invalid_grant" — invalidating the stored tokens and retrying
	// authorization. Cause: "..."`). nil writes them to os.Stderr with a newline, as
	// console.warn does; pass func(string){} to silence them.
	Warn func(string)
}

func (o *Options) warn(msg string) { Warn(o.Warn, msg) }

// Warn delivers an SDK warning to w, or to os.Stderr when w is nil (console.warn).
func Warn(w func(string), msg string) {
	if w != nil {
		w(msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// Auth is auth(): it orchestrates discovery, registration, token refresh, the authorization
// redirect and the code exchange. When the authorization server answers invalid_client or
// unauthorized_client it invalidates the client and tokens and retries once; for invalid_grant
// or invalid_dpop_proof it invalidates the tokens and retries once.
func Auth(ctx context.Context, provider ClientProvider, opts Options) (Result, error) {
	res, err := authInternal(ctx, provider, &opts)
	if err == nil {
		return res, nil
	}
	var oe *OAuthError
	if errors.As(err, &oe) {
		switch oe.Code {
		case ErrInvalidClient, ErrUnauthorizedClient:
			opts.warn(fmt.Sprintf("[mcp-sdk] OAuth %s — invalidating the stored client credentials and tokens and retrying authorization. Cause: %s", jsonString(oe.Code), jsonString(oe.Message)))
			if err := provider.InvalidateCredentials(ctx, InvalidateClient); err != nil {
				return "", err
			}
			if err := provider.InvalidateCredentials(ctx, InvalidateTokens); err != nil {
				return "", err
			}
			return authInternal(ctx, provider, &opts)
		case ErrInvalidGrant, ErrInvalidDpopProof:
			opts.warn(fmt.Sprintf("[mcp-sdk] OAuth %s — invalidating the stored tokens and retrying authorization. Cause: %s", jsonString(oe.Code), jsonString(oe.Message)))
			if err := provider.InvalidateCredentials(ctx, InvalidateTokens); err != nil {
				return "", err
			}
			return authInternal(ctx, provider, &opts)
		}
	}
	return "", err
}

// issuersMatch tolerates a single trailing slash difference (SEP-2352).
func issuersMatch(a, b string) bool {
	return a == b || (strings.HasSuffix(a, "/") && a[:len(a)-1] == b) || (strings.HasSuffix(b, "/") && b[:len(b)-1] == a)
}

// resolveClientMetadata fills grant_types (interactive providers) and application_type
// without overwriting fields the provider set.
func resolveClientMetadata(provider ClientProvider) *jsonv.Object {
	cm := provider.ClientMetadata()
	var out *jsonv.Object
	if cm == nil {
		out = jsonv.NewObject()
	} else {
		out = cm.Clone()
	}
	if v, ok := out.Get("grant_types"); !ok || v == nil || jsonv.IsUndefined(v) {
		if provider.RedirectURL() == "" {
			out.Set("grant_types", jsonv.Undefined)
		} else {
			out.Set("grant_types", []any{"authorization_code", "refresh_token"})
		}
	}
	if v, ok := out.Get("application_type"); !ok || v == nil || jsonv.IsUndefined(v) {
		uris, _ := out.Get("redirect_uris")
		out.Set("application_type", deriveApplicationType(uris))
	}
	return out
}

func isLoopbackHost(hostname string) bool {
	return hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || hostname == "127.0.0.1" || hostname == "[::1]" || hostname == "::1"
}

func deriveApplicationType(uris any) string {
	list, _ := uris.([]any)
	for _, raw := range list {
		rs, ok := raw.(string)
		if !ok {
			rs = describe(raw)
		}
		u, err := jsurl.Parse(rs, nil)
		if err != nil {
			continue
		}
		if u.Protocol() != "http:" && u.Protocol() != "https:" {
			return "native"
		}
		if isLoopbackHost(u.Hostname()) {
			return "native"
		}
	}
	return "web"
}

func determineScope(requested string, rm *ProtectedResourceMetadata, asm *AuthorizationServerMetadata, clientMetadata *jsonv.Object) *string {
	eff := requested
	if eff == "" && rm != nil && len(rm.ScopesSupported) > 0 {
		eff = strings.Join(rm.ScopesSupported, " ")
	}
	if eff == "" && clientMetadata != nil {
		if v, ok := clientMetadata.Get("scope"); ok {
			eff, _ = v.(string)
		}
	}
	if eff == "" {
		return nil
	}
	if asm != nil && contains(asm.ScopesSupported, "offline_access") && !contains(strings.Split(eff, " "), "offline_access") {
		if gt, ok := clientMetadata.Get("grant_types"); ok {
			if list, ok := gt.([]any); ok {
				for _, g := range list {
					if g == "refresh_token" {
						eff += " offline_access"
						break
					}
				}
			}
		}
	}
	return &eff
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func discardClientIfIssuerMismatch(stored *ClientInformation, issuer string, warn func(string), canPersist bool) *ClientInformation {
	if stored == nil {
		return nil
	}
	if stored.Issuer == nil {
		if canPersist {
			warn("[mcp-sdk] SEP-2352: stored OAuth credential has no 'issuer' stamp (pre-upgrade storage or provider not round-tripping the value). SEP-2352 isolation is inactive for this read; ensure your provider round-trips the issuer field.")
		}
		return stored
	}
	if issuersMatch(*stored.Issuer, issuer) {
		return stored
	}
	return nil
}

func discardTokensIfIssuerMismatch(stored *Tokens, issuer string, warn func(string)) *Tokens {
	if stored == nil {
		return nil
	}
	if stored.Issuer == nil {
		warn("[mcp-sdk] SEP-2352: stored OAuth credential has no 'issuer' stamp (pre-upgrade storage or provider not round-tripping the value). SEP-2352 isolation is inactive for this read; ensure your provider round-trips the issuer field.")
		return stored
	}
	if issuersMatch(*stored.Issuer, issuer) {
		return stored
	}
	return nil
}

func authInternal(ctx context.Context, provider ClientProvider, o *Options) (Result, error) {
	clientMetadata := resolveClientMetadata(provider)
	cachedState, err := provider.DiscoveryState(ctx)
	if err != nil {
		return "", err
	}
	var (
		resourceMetadata *ProtectedResourceMetadata
		asURL            string
		metadata         *AuthorizationServerMetadata
		fresh            *DiscoveryState
	)
	effRMURL := o.ResourceMetadataURL
	if effRMURL == nil && cachedState != nil && cachedState.ResourceMetadataURL != nil && *cachedState.ResourceMetadataURL != "" {
		if effRMURL, err = jsurl.Parse(*cachedState.ResourceMetadataURL, nil); err != nil {
			return "", err
		}
	}
	rmURLString := func() *string {
		if effRMURL == nil {
			return nil
		}
		h := effRMURL.Href()
		return &h
	}
	if cachedState != nil && cachedState.AuthorizationServerURL != "" {
		asURL = cachedState.AuthorizationServerURL
		resourceMetadata = cachedState.ResourceMetadata
		metadata = cachedState.AuthorizationServerMetadata
		if metadata == nil {
			if metadata, err = DiscoverAuthorizationServerMetadata(ctx, asURL, AuthorizationServerDiscoveryOptions{Fetch: o.Fetch, SkipIssuerValidation: o.SkipIssuerMetadataValidation}); err != nil {
				return "", err
			}
		}
		if resourceMetadata == nil {
			rm, err := DiscoverOAuthProtectedResourceMetadata(ctx, o.ServerURL, &ProtectedResourceDiscoveryOptions{ResourceMetadataURL: effRMURL}, o.Fetch)
			if err != nil {
				if httpx.IsTypeError(err) {
					return "", err
				}
			} else {
				resourceMetadata = rm
			}
		}
		if metadata != cachedState.AuthorizationServerMetadata || resourceMetadata != cachedState.ResourceMetadata {
			if err := provider.SaveDiscoveryState(ctx, DiscoveryState{AuthorizationServerURL: asURL, ResourceMetadataURL: rmURLString(), ResourceMetadata: resourceMetadata, AuthorizationServerMetadata: metadata}); err != nil {
				return "", err
			}
		}
	} else {
		info, err := DiscoverOAuthServerInfo(ctx, o.ServerURL, ServerInfoOptions{ResourceMetadataURL: effRMURL, Fetch: o.Fetch, SkipIssuerMetadataValidation: o.SkipIssuerMetadataValidation})
		if err != nil {
			return "", err
		}
		asURL, metadata, resourceMetadata = info.AuthorizationServerURL, info.AuthorizationServerMetadata, info.ResourceMetadata
		fresh = &DiscoveryState{AuthorizationServerURL: asURL, ResourceMetadataURL: rmURLString(), ResourceMetadata: resourceMetadata, AuthorizationServerMetadata: metadata}
	}
	issuer := asURL
	if metadata != nil {
		issuer = metadata.Issuer
	}
	infoCtx := &ClientInformationContext{Issuer: issuer}
	if o.AuthorizationCode != nil {
		if cachedState == nil {
			return "", &AuthorizationServerMismatchError{RecordedIssuer: "discoveryState was not available on the callback leg; ensure your provider persists discoveryState alongside codeVerifier", CurrentIssuer: issuer}
		}
		recorded := cachedState.AuthorizationServerURL
		if cachedState.AuthorizationServerMetadata != nil {
			recorded = cachedState.AuthorizationServerMetadata.Issuer
		}
		if !issuersMatch(recorded, issuer) {
			return "", &AuthorizationServerMismatchError{RecordedIssuer: recorded, CurrentIssuer: issuer}
		}
	}
	if fresh != nil {
		if err := provider.SaveDiscoveryState(ctx, *fresh); err != nil {
			return "", err
		}
	}
	serverURL, err := jsurl.Parse(o.ServerURL, nil)
	if err != nil {
		return "", err
	}
	defaultResource := serverURL.Clone()
	defaultResource.SetHash("")
	var rmResource *string
	if resourceMetadata != nil {
		r := resourceMetadata.Resource
		rmResource = &r
	}
	resource, err := provider.ValidateResourceURL(ctx, defaultResource, rmResource)
	if err != nil {
		return "", err
	}
	resolvedScope := determineScope(o.Scope, resourceMetadata, metadata, provider.ClientMetadata())

	rawClient, err := provider.ClientInformation(ctx, infoCtx)
	if err != nil {
		return "", err
	}
	client := discardClientIfIssuerMismatch(rawClient, issuer, o.warn, true)
	if client != nil && client.Issuer == nil {
		stamped := client.withIssuer(issuer)
		client = &stamped
		if err := provider.SaveClientInformation(ctx, stamped, infoCtx); err != nil {
			return "", err
		}
	}
	if client == nil {
		if o.AuthorizationCode != nil {
			return "", errors.New("Existing OAuth client information is required when exchanging an authorization code")
		}
		full, err := registerClient(ctx, asURL, metadata, clientMetadata, resolvedScope, o.Fetch)
		if err != nil {
			return "", err
		}
		stamped := full.withIssuer(issuer)
		client = &stamped
		if err := provider.SaveClientInformation(ctx, stamped, infoCtx); err != nil {
			return "", err
		}
	}

	redirectURL := provider.RedirectURL()
	if o.AuthorizationCode != nil || redirectURL == "" {
		if o.AuthorizationCode != nil {
			if err := validateAuthorizationResponseIssuer(o.Iss, metadata); err != nil {
				return "", err
			}
		}
		tokens, err := fetchToken(ctx, provider, asURL, metadata, resource, o.AuthorizationCode, o.Iss, o.Fetch)
		if err != nil {
			return "", err
		}
		if err := provider.SaveTokens(ctx, tokens.withIssuer(issuer), infoCtx); err != nil {
			return "", err
		}
		return Authorized, nil
	}

	rawTokens, err := provider.Tokens(ctx, infoCtx)
	if err != nil {
		return "", err
	}
	tokens := discardTokensIfIssuerMismatch(rawTokens, issuer, o.warn)
	if tokens != nil && tokens.Issuer == nil {
		stamped := tokens.withIssuer(issuer)
		tokens = &stamped
		if err := provider.SaveTokens(ctx, stamped, infoCtx); err != nil {
			return "", err
		}
	}
	if tokens != nil && tokens.RefreshToken != nil && *tokens.RefreshToken != "" && !o.ForceReauthorization {
		newTokens, err := refreshAuthorization(ctx, asURL, metadata, client, *tokens.RefreshToken, resource, o.Fetch)
		if err != nil {
			var insecure *InsecureTokenEndpointError
			var oe *OAuthError
			if errors.As(err, &insecure) {
				return "", err
			}
			if !errors.As(err, &oe) || oe.Code == ErrServerError {
				o.warn("[mcp-sdk] Could not refresh OAuth tokens; falling back to a new authorization request. Cause: " + jsonString(err.Error()))
			} else {
				return "", err
			}
		}
		if newTokens != nil {
			if err := provider.SaveTokens(ctx, newTokens.withIssuer(issuer), infoCtx); err != nil {
				return "", err
			}
			return Authorized, nil
		}
	}

	state, err := provider.State(ctx)
	if err != nil {
		return "", err
	}
	authURL, verifier, err := startAuthorization(asURL, metadata, client, redirectURL, resolvedScope, state, resource)
	if err != nil {
		return "", err
	}
	if err := provider.SaveCodeVerifier(ctx, verifier); err != nil {
		return "", err
	}
	if err := provider.RedirectToAuthorization(ctx, authURL); err != nil {
		return "", err
	}
	return Redirect, nil
}

// validateAuthorizationResponseIssuer implements RFC 9207 §2.4 with the MCP decision table.
func validateAuthorizationResponseIssuer(iss *string, metadata *AuthorizationServerMetadata) error {
	if metadata == nil {
		return nil
	}
	expected := metadata.Issuer
	supported := metadata.AuthorizationResponseIssParameterSupported != nil && *metadata.AuthorizationResponseIssParameterSupported
	if iss == nil {
		if supported {
			return &IssuerMismatchError{Kind: "authorization_response", Expected: expected}
		}
		return nil
	}
	if *iss != expected {
		received := *iss
		return &IssuerMismatchError{Kind: "authorization_response", Expected: expected, Received: &received}
	}
	return nil
}

// ValidateAuthorizationResponseIssuer is the exported validateAuthorizationResponseIssuer.
func ValidateAuthorizationResponseIssuer(iss *string, metadata *AuthorizationServerMetadata) error {
	return validateAuthorizationResponseIssuer(iss, metadata)
}

const pkceMask = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"

// GeneratePKCE returns a 43-character code verifier and its S256 challenge (pkce-challenge 5).
func GeneratePKCE() (verifier, challenge string, err error) {
	cutoff := 256 - 256%len(pkceMask)
	var sb strings.Builder
	for sb.Len() < 43 {
		buf := make([]byte, 43-sb.Len())
		if _, err := rand.Read(buf); err != nil {
			return "", "", err
		}
		for _, b := range buf {
			if int(b) < cutoff {
				sb.WriteByte(pkceMask[int(b)%len(pkceMask)])
			}
		}
	}
	verifier = sb.String()
	return verifier, PKCEChallenge(verifier), nil
}

// PKCEChallenge is the S256 code challenge for a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func startAuthorization(asURL string, metadata *AuthorizationServerMetadata, client *ClientInformation, redirectURL string, scope *string, state string, resource *jsurl.URL) (*jsurl.URL, string, error) {
	var authURL *jsurl.URL
	var err error
	if metadata != nil {
		if authURL, err = jsurl.Parse(metadata.AuthorizationEndpoint, nil); err != nil {
			return nil, "", err
		}
		if !contains(metadata.ResponseTypesSupported, "code") {
			return nil, "", errors.New("Incompatible auth server: does not support response type code")
		}
		if metadata.CodeChallengeMethodsSupported != nil && !contains(metadata.CodeChallengeMethodsSupported, "S256") {
			return nil, "", errors.New("Incompatible auth server: does not support code challenge method S256")
		}
	} else if authURL, err = jsurl.ParseRef("/authorize", asURL); err != nil {
		return nil, "", err
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, "", err
	}
	sp := authURL.SearchParams()
	sp.Set("response_type", "code")
	sp.Set("client_id", client.ClientID)
	sp.Set("code_challenge", challenge)
	sp.Set("code_challenge_method", "S256")
	sp.Set("redirect_uri", redirectURL)
	if state != "" {
		sp.Set("state", state)
	}
	if scope != nil && *scope != "" {
		sp.Set("scope", *scope)
	}
	if scope != nil && contains(strings.Split(*scope, " "), "offline_access") {
		sp.Append("prompt", "consent")
	}
	if resource != nil {
		sp.Set("resource", resource.Href())
	}
	return authURL, verifier, nil
}

// SelectClientAuthMethod is selectClientAuthMethod.
func SelectClientAuthMethod(client *ClientInformation, supported []string) string {
	hasSecret := client.ClientSecret != nil
	if m := client.TokenEndpointAuthMethod; m != nil && (*m == "client_secret_basic" || *m == "client_secret_post" || *m == "none") && (len(supported) == 0 || contains(supported, *m)) {
		return *m
	}
	if len(supported) == 0 {
		if hasSecret {
			return "client_secret_basic"
		}
		return "none"
	}
	if hasSecret && contains(supported, "client_secret_basic") {
		return "client_secret_basic"
	}
	if hasSecret && contains(supported, "client_secret_post") {
		return "client_secret_post"
	}
	if contains(supported, "none") {
		return "none"
	}
	if hasSecret {
		return "client_secret_post"
	}
	return "none"
}

// btoa encodes a Latin-1 string; characters above U+00FF fail as in JavaScript.
func btoa(s string) (string, error) {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return "", errors.New("InvalidCharacterError: Invalid character")
		}
		b = append(b, byte(r))
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

type form struct{ list [][2]string }

func (f *form) set(k, v string) {
	for i := range f.list {
		if f.list[i][0] == k {
			f.list[i][1] = v
			out := f.list[:i+1]
			for _, kv := range f.list[i+1:] {
				if kv[0] != k {
					out = append(out, kv)
				}
			}
			f.list = out
			return
		}
	}
	f.list = append(f.list, [2]string{k, v})
}

func applyClientAuthentication(method string, client *ClientInformation, h http.Header, params *form) error {
	switch method {
	case "client_secret_basic":
		if client.ClientSecret == nil || *client.ClientSecret == "" {
			return errors.New("client_secret_basic authentication requires a client_secret")
		}
		creds, err := btoa(client.ClientID + ":" + *client.ClientSecret)
		if err != nil {
			return err
		}
		h.Set("Authorization", "Basic "+creds)
	case "client_secret_post":
		params.set("client_id", client.ClientID)
		if client.ClientSecret != nil && *client.ClientSecret != "" {
			params.set("client_secret", *client.ClientSecret)
		}
	case "none":
		params.set("client_id", client.ClientID)
	default:
		return fmt.Errorf("Unsupported client authentication method: %s", method)
	}
	return nil
}

// AssertSecureTokenEndpoint refuses a token endpoint that is neither https nor loopback.
func AssertSecureTokenEndpoint(endpoint string) (*jsurl.URL, error) {
	u, err := jsurl.Parse(endpoint, nil)
	if err != nil {
		return nil, err
	}
	if u.Protocol() != "https:" && !isLoopbackHost(u.Hostname()) {
		return nil, &InsecureTokenEndpointError{TokenEndpoint: u.Href()}
	}
	return u, nil
}

func executeTokenRequest(ctx context.Context, asURL string, metadata *AuthorizationServerMetadata, params *form, client *ClientInformation, resource *jsurl.URL, fetch httpx.Fetch) (*Tokens, error) {
	endpoint := ""
	if metadata != nil {
		endpoint = metadata.TokenEndpoint
	} else {
		u, err := jsurl.ParseRef("/token", asURL)
		if err != nil {
			return nil, err
		}
		endpoint = u.Href()
	}
	tokenURL, err := AssertSecureTokenEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Accept", "application/json")
	if resource != nil {
		params.set("resource", resource.Href())
	}
	if client != nil {
		var supported []string
		if metadata != nil {
			supported = metadata.TokenEndpointAuthMethodsSupported
		}
		if err := applyClientAuthentication(SelectClientAuthMethod(client, supported), client, h, params); err != nil {
			return nil, err
		}
	}
	body := []byte(jsurl.SerializeQuery(params.list))
	resp, err := httpx.WithinOrigin(fetch)(ctx, tokenURL.Href(), &httpx.RequestInit{Method: "POST", Header: h, Body: body})
	if err != nil {
		return nil, err
	}
	if !httpx.OK(resp) {
		text, err := httpx.Text(resp)
		if err != nil {
			return nil, err
		}
		return nil, parseErrorResponse(resp.StatusCode, text)
	}
	data, err := httpx.JSON(resp)
	if err != nil {
		return nil, err
	}
	tokens, parseErr := ParseTokens(withoutIssuer(data))
	if parseErr != nil {
		if o, ok := data.(*jsonv.Object); ok && o.Has("error") {
			return nil, parseErrorResponse(0, jsonv.MustStringify(data))
		}
		return nil, parseErr
	}
	return tokens, nil
}

func refreshAuthorization(ctx context.Context, asURL string, metadata *AuthorizationServerMetadata, client *ClientInformation, refreshToken string, resource *jsurl.URL, fetch httpx.Fetch) (*Tokens, error) {
	params := &form{list: [][2]string{{"grant_type", "refresh_token"}, {"refresh_token", refreshToken}}}
	tokens, err := executeTokenRequest(ctx, asURL, metadata, params, client, resource, fetch)
	if err != nil {
		return nil, err
	}
	// {refresh_token: refreshToken, ...tokens}: a new refresh token replaces the old one.
	if tokens.RefreshToken == nil {
		tokens.RefreshToken = &refreshToken
	}
	return tokens, nil
}

func fetchToken(ctx context.Context, provider ClientProvider, asURL string, metadata *AuthorizationServerMetadata, resource *jsurl.URL, code, iss *string, fetch httpx.Fetch) (*Tokens, error) {
	if code != nil {
		if err := validateAuthorizationResponseIssuer(iss, metadata); err != nil {
			return nil, err
		}
	}
	issuer := asURL
	if metadata != nil {
		issuer = metadata.Issuer
	}
	readClient := func() (*ClientInformation, error) {
		raw, err := provider.ClientInformation(ctx, &ClientInformationContext{Issuer: issuer})
		if err != nil {
			return nil, err
		}
		checked := discardClientIfIssuerMismatch(raw, issuer, func(string) {}, false)
		if raw != nil && checked == nil {
			recorded := ""
			if raw.Issuer != nil {
				recorded = *raw.Issuer
			}
			return nil, &AuthorizationServerMismatchError{RecordedIssuer: recorded, CurrentIssuer: issuer}
		}
		return checked, nil
	}
	client, err := readClient()
	if err != nil {
		return nil, err
	}
	if code == nil || *code == "" {
		return nil, errors.New("Either provider.prepareTokenRequest() or authorizationCode is required")
	}
	redirectURL := provider.RedirectURL()
	if redirectURL == "" {
		return nil, errors.New("redirectUrl is required for authorization_code flow")
	}
	verifier, err := provider.CodeVerifier(ctx)
	if err != nil {
		return nil, err
	}
	params := &form{list: [][2]string{{"grant_type", "authorization_code"}, {"code", *code}, {"code_verifier", verifier}, {"redirect_uri", redirectURL}}}
	if client == nil {
		if client, err = readClient(); err != nil {
			return nil, err
		}
	}
	return executeTokenRequest(ctx, asURL, metadata, params, client, resource, fetch)
}

func registerClient(ctx context.Context, asURL string, metadata *AuthorizationServerMetadata, clientMetadata *jsonv.Object, scope *string, fetch httpx.Fetch) (*ClientInformation, error) {
	var registrationURL string
	if metadata != nil {
		if metadata.RegistrationEndpoint == nil {
			return nil, errors.New("Incompatible auth server: does not support dynamic client registration")
		}
		u, err := jsurl.Parse(*metadata.RegistrationEndpoint, nil)
		if err != nil {
			return nil, err
		}
		registrationURL = u.Href()
	} else {
		u, err := jsurl.ParseRef("/register", asURL)
		if err != nil {
			return nil, err
		}
		registrationURL = u.Href()
	}
	submitted := clientMetadata.Clone()
	if scope != nil {
		submitted.Set("scope", *scope)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	body, err := jsonv.Stringify(submitted)
	if err != nil {
		return nil, err
	}
	resp, err := httpx.WithinOrigin(fetch)(ctx, registrationURL, &httpx.RequestInit{Method: "POST", Header: h, Body: body})
	if err != nil {
		return nil, err
	}
	if !httpx.OK(resp) {
		text, err := httpx.Text(resp)
		if err != nil {
			return nil, err
		}
		return nil, &RegistrationRejectedError{Status: resp.StatusCode, Body: text, SubmittedMetadata: submitted}
	}
	data, err := httpx.JSON(resp)
	if err != nil {
		return nil, err
	}
	return ParseClientInformationFull(withoutIssuer(data))
}

// WWWAuthenticateParams are the parameters extractWWWAuthenticateParams reads.
type WWWAuthenticateParams struct {
	ResourceMetadataURL *jsurl.URL
	Scope               string
	Error               string
	ErrorDescription    string
}

const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var wwwAuthFieldPatterns = map[string]*regexp.Regexp{}

func init() {
	for _, f := range []string{"resource_metadata", "scope", "error", "error_description"} {
		wwwAuthFieldPatterns[f] = regexp.MustCompile(f + `=(?:"([^"]+)"|([^` + jsSpace + `,]+))`)
	}
}

func extractField(header, field string) string {
	m := wwwAuthFieldPatterns[field].FindStringSubmatch(header)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// ExtractWWWAuthenticateParams is extractWWWAuthenticateParams for the response's
// WWW-Authenticate header(s) (Bearer or DPoP scheme).
func ExtractWWWAuthenticateParams(h http.Header) WWWAuthenticateParams {
	header := strings.Join(h.Values("WWW-Authenticate"), ", ")
	if header == "" {
		return WWWAuthenticateParams{}
	}
	parts := strings.Split(header, " ")
	typ := parts[0]
	scheme := ""
	if len(parts) > 1 {
		scheme = parts[1]
	}
	if typ == "" || (strings.ToLower(typ) != "bearer" && strings.ToLower(typ) != "dpop") || scheme == "" {
		return WWWAuthenticateParams{}
	}
	var out WWWAuthenticateParams
	if rm := extractField(header, "resource_metadata"); rm != "" {
		if u, err := jsurl.Parse(rm, nil); err == nil {
			out.ResourceMetadataURL = u
		}
	}
	out.Scope = extractField(header, "scope")
	out.Error = extractField(header, "error")
	out.ErrorDescription = extractField(header, "error_description")
	return out
}

// ComputeScopeUnion is computeScopeUnion: distinct scope tokens in first-seen order.
func ComputeScopeUnion(scopes ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, sc := range scopes {
		for _, tok := range strings.FieldsFunc(sc, isJSSpace) {
			if !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
		}
	}
	return strings.Join(out, " ")
}

// IsStrictScopeSuperset reports whether union has a token current lacks.
func IsStrictScopeSuperset(union, current string) bool {
	if union == "" {
		return false
	}
	have := map[string]bool{}
	for _, tok := range strings.FieldsFunc(current, isJSSpace) {
		have[tok] = true
	}
	for _, tok := range strings.FieldsFunc(union, isJSSpace) {
		if !have[tok] {
			return true
		}
	}
	return false
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// UnauthorizedContext carries what a 401 handler needs.
type UnauthorizedContext struct {
	Response  *http.Response
	ServerURL string
	Fetch     httpx.Fetch
}

// HandleOptions are the extra auth options a transport passes on a 401.
type HandleOptions struct {
	SkipIssuerMetadataValidation bool
	Warn                         func(string) // see Options.Warn
}

// HandleUnauthorized is handleOAuthUnauthorized: run Auth with the challenge's resource
// metadata URL and scope; anything but AUTHORIZED is an UnauthorizedError.
func HandleUnauthorized(ctx context.Context, provider ClientProvider, uc UnauthorizedContext, opts HandleOptions) error {
	params := ExtractWWWAuthenticateParams(uc.Response.Header)
	res, err := Auth(ctx, provider, Options{
		ServerURL:                    uc.ServerURL,
		ResourceMetadataURL:          params.ResourceMetadataURL,
		Scope:                        params.Scope,
		Fetch:                        uc.Fetch,
		SkipIssuerMetadataValidation: opts.SkipIssuerMetadataValidation,
		Warn:                         opts.Warn,
	})
	if err != nil {
		return err
	}
	if res != Authorized {
		return NewUnauthorizedError()
	}
	return nil
}
