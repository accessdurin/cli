package mcp

import (
	"github.com/accessdurin/cli/internal/jsjson"
)

// SdkErrorCode is SdkErrorCode: local errors that never cross the wire.
type SdkErrorCode string

// SDK error codes.
const (
	ErrNotConnected                     SdkErrorCode = "NOT_CONNECTED"
	ErrAlreadyConnected                 SdkErrorCode = "ALREADY_CONNECTED"
	ErrNotInitialized                   SdkErrorCode = "NOT_INITIALIZED"
	ErrCapabilityNotSupported           SdkErrorCode = "CAPABILITY_NOT_SUPPORTED"
	ErrRequestTimeout                   SdkErrorCode = "REQUEST_TIMEOUT"
	ErrConnectionClosed                 SdkErrorCode = "CONNECTION_CLOSED"
	ErrSendFailed                       SdkErrorCode = "SEND_FAILED"
	ErrInvalidResult                    SdkErrorCode = "INVALID_RESULT"
	ErrUnsupportedResultType            SdkErrorCode = "UNSUPPORTED_RESULT_TYPE"
	ErrListPaginationExceeded           SdkErrorCode = "LIST_PAGINATION_EXCEEDED"
	ErrMethodNotSupportedByProtocol     SdkErrorCode = "METHOD_NOT_SUPPORTED_BY_PROTOCOL_VERSION"
	ErrEraNegotiationFailed             SdkErrorCode = "ERA_NEGOTIATION_FAILED"
	ErrClientHTTPNotImplemented         SdkErrorCode = "CLIENT_HTTP_NOT_IMPLEMENTED"
	ErrClientHTTPAuthentication         SdkErrorCode = "CLIENT_HTTP_AUTHENTICATION"
	ErrClientHTTPForbidden              SdkErrorCode = "CLIENT_HTTP_FORBIDDEN"
	ErrClientHTTPUnexpectedContent      SdkErrorCode = "CLIENT_HTTP_UNEXPECTED_CONTENT"
	ErrClientHTTPFailedToOpenStream     SdkErrorCode = "CLIENT_HTTP_FAILED_TO_OPEN_STREAM"
	ErrClientHTTPFailedToTerminateSessn SdkErrorCode = "CLIENT_HTTP_FAILED_TO_TERMINATE_SESSION"
)

// SdkError is the SDK's SdkError. Data mirrors the error's data payload (for example
// {"timeout": 15000} for a request timeout).
type SdkError struct {
	Code    SdkErrorCode
	Message string
	Data    map[string]any
	Cause   error
}

func (e *SdkError) Error() string { return e.Message }
func (e *SdkError) Unwrap() error { return e.Cause }

// SdkHTTPError is the SDK's SdkHttpError: an HTTP transport failure with a status.
type SdkHTTPError struct {
	SdkError
	Status     int
	StatusText string
	Text       *string // response body when the SDK captured it
}

func (e *SdkHTTPError) Error() string { return e.Message }

// Protocol error codes (ProtocolErrorCode).
const (
	CodeParseError                      = -32700
	CodeInvalidRequest                  = -32600
	CodeMethodNotFound                  = -32601
	CodeInvalidParams                   = -32602
	CodeInternalError                   = -32603
	CodeResourceNotFound                = -32002
	CodeMissingRequiredClientCapability = -32021
	CodeUnsupportedProtocolVersion      = -32022
	CodeURLElicitationRequired          = -32042
)

// ProtocolError is a JSON-RPC error response from the peer (ProtocolError.fromError). The SDK
// maps some codes to subclasses (ResourceNotFoundError, UnsupportedProtocolVersionError, ...);
// this port keeps them all as ProtocolError with the same Code, Message and Data.
type ProtocolError struct {
	Code    int
	Message string
	Data    jsjson.Value // nil when absent
	HasData bool
}

func (e *ProtocolError) Error() string { return e.Message }

func timeoutError(timeout int64) *SdkError {
	return &SdkError{Code: ErrRequestTimeout, Message: "Request timed out", Data: map[string]any{"timeout": timeout}}
}

// jsErrorString renders an error as String(error) does in JavaScript ("Name: message").
func jsErrorString(err error) string {
	switch e := err.(type) {
	case *SdkHTTPError:
		return "SdkHttpError: " + e.Message
	case *SdkError:
		return "SdkError: " + e.Message
	case *ProtocolError:
		return "ProtocolError: " + e.Message
	case *jsjson.SyntaxError:
		return "SyntaxError: " + e.Error()
	}
	return "Error: " + err.Error()
}
