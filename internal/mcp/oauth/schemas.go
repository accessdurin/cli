package oauth

import (
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	s "github.com/accessdurin/cli/internal/mcp/schema"
)

// The schemas below are ports of @modelcontextprotocol/core 2.3.1 src/auth.ts.

var strArr = func() s.Schema { return s.Array(s.String()) }

// ProtectedResourceMetadataSchema is OAuthProtectedResourceMetadataSchema (RFC 9728).
var ProtectedResourceMetadataSchema = s.LooseObject(
	s.F("resource", s.URL()),
	s.F("authorization_servers", s.Optional(s.Array(s.SafeURL()))),
	s.F("jwks_uri", s.Optional(s.URL())),
	s.F("scopes_supported", s.Optional(strArr())),
	s.F("bearer_methods_supported", s.Optional(strArr())),
	s.F("resource_signing_alg_values_supported", s.Optional(strArr())),
	s.F("resource_name", s.Optional(s.String())),
	s.F("resource_documentation", s.Optional(s.String())),
	s.F("resource_policy_uri", s.Optional(s.URL())),
	s.F("resource_tos_uri", s.Optional(s.URL())),
	s.F("tls_client_certificate_bound_access_tokens", s.Optional(s.Boolean())),
	s.F("authorization_details_types_supported", s.Optional(strArr())),
	s.F("dpop_signing_alg_values_supported", s.Optional(strArr())),
	s.F("dpop_bound_access_tokens_required", s.Optional(s.Boolean())),
)

// AuthorizationServerMetadataSchema is OAuthMetadataSchema (RFC 8414).
var AuthorizationServerMetadataSchema = s.LooseObject(
	s.F("issuer", s.String()),
	s.F("authorization_endpoint", s.SafeURL()),
	s.F("token_endpoint", s.SafeURL()),
	s.F("registration_endpoint", s.Optional(s.SafeURL())),
	s.F("scopes_supported", s.Optional(strArr())),
	s.F("response_types_supported", strArr()),
	s.F("response_modes_supported", s.Optional(strArr())),
	s.F("grant_types_supported", s.Optional(strArr())),
	s.F("token_endpoint_auth_methods_supported", s.Optional(strArr())),
	s.F("token_endpoint_auth_signing_alg_values_supported", s.Optional(strArr())),
	s.F("service_documentation", s.Optional(s.SafeURL())),
	s.F("revocation_endpoint", s.Optional(s.SafeURL())),
	s.F("revocation_endpoint_auth_methods_supported", s.Optional(strArr())),
	s.F("revocation_endpoint_auth_signing_alg_values_supported", s.Optional(strArr())),
	s.F("introspection_endpoint", s.Optional(s.String())),
	s.F("introspection_endpoint_auth_methods_supported", s.Optional(strArr())),
	s.F("introspection_endpoint_auth_signing_alg_values_supported", s.Optional(strArr())),
	s.F("code_challenge_methods_supported", s.Optional(strArr())),
	s.F("client_id_metadata_document_supported", s.Optional(s.Boolean())),
	s.F("authorization_response_iss_parameter_supported", s.Catch(s.Optional(s.Boolean()), s.Undefined)),
	s.F("dpop_signing_alg_values_supported", s.Optional(strArr())),
)

// openIDProviderMetadataSchema is OpenIdProviderMetadataSchema.
var openIDProviderMetadataSchema = s.LooseObject(
	s.F("issuer", s.String()),
	s.F("authorization_endpoint", s.SafeURL()),
	s.F("token_endpoint", s.SafeURL()),
	s.F("userinfo_endpoint", s.Optional(s.SafeURL())),
	s.F("jwks_uri", s.SafeURL()),
	s.F("registration_endpoint", s.Optional(s.SafeURL())),
	s.F("scopes_supported", s.Optional(strArr())),
	s.F("response_types_supported", strArr()),
	s.F("response_modes_supported", s.Optional(strArr())),
	s.F("grant_types_supported", s.Optional(strArr())),
	s.F("acr_values_supported", s.Optional(strArr())),
	s.F("subject_types_supported", strArr()),
	s.F("id_token_signing_alg_values_supported", strArr()),
	s.F("id_token_encryption_alg_values_supported", s.Optional(strArr())),
	s.F("id_token_encryption_enc_values_supported", s.Optional(strArr())),
	s.F("userinfo_signing_alg_values_supported", s.Optional(strArr())),
	s.F("userinfo_encryption_alg_values_supported", s.Optional(strArr())),
	s.F("userinfo_encryption_enc_values_supported", s.Optional(strArr())),
	s.F("request_object_signing_alg_values_supported", s.Optional(strArr())),
	s.F("request_object_encryption_alg_values_supported", s.Optional(strArr())),
	s.F("request_object_encryption_enc_values_supported", s.Optional(strArr())),
	s.F("token_endpoint_auth_methods_supported", s.Optional(strArr())),
	s.F("token_endpoint_auth_signing_alg_values_supported", s.Optional(strArr())),
	s.F("display_values_supported", s.Optional(strArr())),
	s.F("claim_types_supported", s.Optional(strArr())),
	s.F("claims_supported", s.Optional(strArr())),
	s.F("service_documentation", s.Optional(s.String())),
	s.F("claims_locales_supported", s.Optional(strArr())),
	s.F("ui_locales_supported", s.Optional(strArr())),
	s.F("claims_parameter_supported", s.Optional(s.Boolean())),
	s.F("request_parameter_supported", s.Optional(s.Boolean())),
	s.F("request_uri_parameter_supported", s.Optional(s.Boolean())),
	s.F("require_request_uri_registration", s.Optional(s.Boolean())),
	s.F("op_policy_uri", s.Optional(s.SafeURL())),
	s.F("op_tos_uri", s.Optional(s.SafeURL())),
	s.F("client_id_metadata_document_supported", s.Optional(s.Boolean())),
	s.F("authorization_response_iss_parameter_supported", s.Catch(s.Optional(s.Boolean()), s.Undefined)),
)

// OpenIDProviderDiscoveryMetadataSchema is OpenIdProviderDiscoveryMetadataSchema. Note that the
// SDK builds it with z.object over the loose shape, so it STRIPS undeclared keys (unlike the
// OAuth metadata schema).
var OpenIDProviderDiscoveryMetadataSchema = s.Object(append(openIDProviderMetadataSchema.Fields(),
	s.F("code_challenge_methods_supported", s.Optional(strArr())))...)

// TokensSchema is OAuthTokensSchema.
var TokensSchema = s.Object(
	s.F("access_token", s.String()),
	s.F("id_token", s.Optional(s.String())),
	s.F("token_type", s.String()),
	s.F("expires_in", s.Optional(s.CoerceNumber())),
	s.F("scope", s.Optional(s.String())),
	s.F("refresh_token", s.Optional(s.String())),
	s.F("issuer", s.Catch(s.Optional(s.String()), s.Undefined)),
)

// ErrorResponseSchema is OAuthErrorResponseSchema.
var ErrorResponseSchema = s.Object(
	s.F("error", s.String()),
	s.F("error_description", s.Optional(s.String())),
	s.F("error_uri", s.Optional(s.String())),
)

// ClientMetadataSchema is OAuthClientMetadataSchema (RFC 7591).
var ClientMetadataSchema = s.Object(
	s.F("redirect_uris", s.Array(s.SafeURL())),
	s.F("token_endpoint_auth_method", s.Optional(s.String())),
	s.F("grant_types", s.Optional(strArr())),
	s.F("response_types", s.Optional(strArr())),
	s.F("application_type", s.Optional(s.String())),
	s.F("client_name", s.Optional(s.String())),
	s.F("client_uri", s.Optional(s.SafeURL())),
	s.F("logo_uri", s.OptionalSafeURL()),
	s.F("scope", s.Optional(s.String())),
	s.F("contacts", s.Optional(strArr())),
	s.F("tos_uri", s.OptionalSafeURL()),
	s.F("policy_uri", s.Optional(s.String())),
	s.F("jwks_uri", s.Optional(s.SafeURL())),
	s.F("jwks", s.Optional(s.Unknown())),
	s.F("software_id", s.Optional(s.String())),
	s.F("software_version", s.Optional(s.String())),
	s.F("software_statement", s.Optional(s.String())),
)

// ClientInformationSchema is OAuthClientInformationSchema.
var ClientInformationSchema = s.Object(
	s.F("client_id", s.String()),
	s.F("client_secret", s.Optional(s.String())),
	s.F("client_id_issued_at", s.Optional(s.Number())),
	s.F("client_secret_expires_at", s.Optional(s.Number())),
	s.F("issuer", s.Catch(s.Optional(s.String()), s.Undefined)),
)

// ClientInformationFullSchema is OAuthClientInformationFullSchema (metadata merged with
// client information).
var ClientInformationFullSchema = ClientMetadataSchema.Extend(ClientInformationSchema.Fields()...)

// withoutIssuer returns an authorization-server body without `issuer` (SEP-2352).
func withoutIssuer(body any) any {
	o, ok := body.(*jsonv.Object)
	if !ok || o == nil {
		return body
	}
	c := o.Clone()
	c.Delete("issuer")
	return c
}
