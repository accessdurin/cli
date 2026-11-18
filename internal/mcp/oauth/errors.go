package oauth

import (
	"fmt"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
)

// Error codes (OAuthErrorCode).
const (
	ErrInvalidRequest          = "invalid_request"
	ErrInvalidClient           = "invalid_client"
	ErrInvalidGrant            = "invalid_grant"
	ErrUnauthorizedClient      = "unauthorized_client"
	ErrUnsupportedGrantType    = "unsupported_grant_type"
	ErrInvalidScope            = "invalid_scope"
	ErrAccessDenied            = "access_denied"
	ErrServerError             = "server_error"
	ErrTemporarilyUnavailable  = "temporarily_unavailable"
	ErrUnsupportedResponseType = "unsupported_response_type"
	ErrUnsupportedTokenType    = "unsupported_token_type"
	ErrInvalidToken            = "invalid_token"
	ErrMethodNotAllowed        = "method_not_allowed"
	ErrTooManyRequests         = "too_many_requests"
	ErrInvalidClientMetadata   = "invalid_client_metadata"
	ErrInvalidRedirectURI      = "invalid_redirect_uri"
	ErrInsufficientScope       = "insufficient_scope"
	ErrInvalidTarget           = "invalid_target"
	ErrInvalidDpopProof        = "invalid_dpop_proof"
	ErrUseDpopNonce            = "use_dpop_nonce"
)

// OAuthError is the SDK's OAuthError: an OAuth protocol error from the authorization server.
type OAuthError struct {
	Code     string
	Message  string
	ErrorURI string
}

func (e *OAuthError) Error() string { return e.Message }

// UnauthorizedError is the SDK's UnauthorizedError.
type UnauthorizedError struct{ Message string }

func (e *UnauthorizedError) Error() string {
	if e.Message == "" {
		return "Unauthorized"
	}
	return e.Message
}

// NewUnauthorizedError returns an UnauthorizedError with the default message "Unauthorized".
func NewUnauthorizedError() *UnauthorizedError { return &UnauthorizedError{} }

// IssuerMismatchError is thrown when an issuer identifier fails validation: kind "metadata"
// (RFC 8414 §3.3 echo) or "authorization_response" (RFC 9207 iss parameter). Received is
// attacker-controllable on the authorization_response path and must not be displayed.
type IssuerMismatchError struct {
	Kind     string
	Expected string
	Received *string
}

func (e *IssuerMismatchError) Error() string {
	where := "authorization response (RFC 9207)"
	if e.Kind == "metadata" {
		where = "authorization server metadata (RFC 8414 §3.3)"
	}
	received := "undefined"
	if e.Received != nil {
		received = jsonString(*e.Received)
	}
	return fmt.Sprintf("Issuer mismatch in %s: expected %s, received %s", where, jsonString(e.Expected), received)
}

// RegistrationRejectedError is thrown when dynamic client registration is refused.
type RegistrationRejectedError struct {
	Status            int
	Body              string
	SubmittedMetadata *jsjson.Object
}

func (e *RegistrationRejectedError) Error() string {
	return fmt.Sprintf("Dynamic Client Registration rejected (HTTP %d): %s", e.Status, e.Body)
}

// InsecureTokenEndpointError is thrown for a non-https, non-loopback token endpoint.
type InsecureTokenEndpointError struct{ TokenEndpoint string }

func (e *InsecureTokenEndpointError) Error() string {
	return fmt.Sprintf("Refusing to send credentials to non-https token endpoint '%s'. OAuth token requests MUST use TLS (localhost / *.localhost / 127.0.0.1 / ::1 are exempt).", e.TokenEndpoint)
}

// AuthorizationServerMismatchError is thrown when the authorization server in use differs from
// the one a credential (or the pending authorization code) is bound to.
type AuthorizationServerMismatchError struct {
	RecordedIssuer string
	CurrentIssuer  string
}

func (e *AuthorizationServerMismatchError) Error() string {
	return fmt.Sprintf("Authorization server mismatch: credentials are bound to %s but this call resolved %s; refusing to present them to a different authorization server", jsonString(e.RecordedIssuer), jsonString(e.CurrentIssuer))
}

// InsufficientScopeError is thrown by the HTTP transport for 403 insufficient_scope when
// onInsufficientScope is 'throw' (or no OAuth provider can step up).
type InsufficientScopeError struct {
	RequiredScope       string
	ResourceMetadataURL string
	ErrorDescription    string
}

func (e *InsufficientScopeError) Error() string {
	if e.RequiredScope != "" {
		return fmt.Sprintf("Insufficient scope: required %q", e.RequiredScope)
	}
	return "Insufficient scope"
}

// jsonString is JSON.stringify for a string.
func jsonString(v string) string { return jsjson.Quote(v) }

// parseErrorResponse is the SDK's parseErrorResponse: a standard OAuth error body becomes an
// OAuthError with its code; anything else becomes a server_error carrying the status and body.
func parseErrorResponse(status int, body string) *OAuthError {
	v, err := jsjson.ParseString(body)
	if err == nil {
		var parsed jsjson.Value
		parsed, err = parseWith(ErrorResponseSchema, v)
		if err == nil {
			o := parsed.(*jsjson.Object)
			desc := optStr(o, "error_description")
			code := str(o, "error")
			msg := code
			if desc != nil {
				msg = *desc
			}
			uri := ""
			if u := optStr(o, "error_uri"); u != nil {
				uri = *u
			}
			return &OAuthError{Code: code, Message: msg, ErrorURI: uri}
		}
	}
	prefix := ""
	if status != 0 {
		prefix = fmt.Sprintf("HTTP %d: ", status)
	}
	return &OAuthError{Code: ErrServerError, Message: fmt.Sprintf("%sInvalid OAuth error response: %s. Raw body: %s", prefix, jsError(err), body)}
}

// jsError renders an error as String(error) would in JavaScript.
func jsError(err error) string {
	if err == nil {
		return "Error"
	}
	switch err.(type) {
	case *jsjson.SyntaxError:
		return "SyntaxError: " + err.Error()
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "[") {
		return "ZodError: " + msg
	}
	return "Error: " + msg
}
