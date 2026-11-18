package mcp

import (
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	s "github.com/accessdurin/cli/internal/mcp/schema"
)

// Message is a JSON-RPC message as the SDK holds it: a JSON object in the key order zod's
// JSONRPCMessageSchema.parse produces.
//
// IMPORTANT for the bridge: the SDK does NOT pass messages through byte-for-byte. Both the
// stdio server transport (ReadBuffer → deserializeMessage) and the Streamable HTTP client
// transport (JSON and SSE bodies) run JSONRPCMessageSchema.parse, whose output is what reaches
// onmessage and is re-serialized with JSON.stringify. That parse:
//   - reorders envelope keys to jsonrpc, id, method, params / result / error;
//   - in params._meta moves progressToken and io.modelcontextprotocol/related-task first and
//     strips unknown keys inside related-task;
//   - in result._meta moves io.modelcontextprotocol/serverInfo first, strips unknown keys inside
//     it, and drops it entirely when it is not a valid Implementation;
//   - in error objects keeps only code, message and data (in that order);
//   - keeps every other params/result key, in input order.
//
// ParseMessage reproduces exactly that. Integration with internal/jsjson: Message is a
// *jsonv.Object; swapping jsonv for jsjson only changes this alias.
type Message = *jsonv.Object

// ParseMessage is JSONRPCMessageSchema.parse: it validates a JSON value as one of the four
// strict JSON-RPC message shapes (first match wins: request, notification, result, error) and
// returns zod's parsed output.
func ParseMessage(v any) (Message, error) {
	out, err := s.Parse(JSONRPCMessageSchema, v)
	if err != nil {
		return nil, err
	}
	return out.(*jsonv.Object), nil
}

// ParseMessageJSON parses JSON text (JSON.parse) and then ParseMessage.
func ParseMessageJSON(data []byte) (Message, error) {
	v, err := jsonv.Parse(data)
	if err != nil {
		return nil, err
	}
	return ParseMessage(v)
}

// SerializeMessage is JSON.stringify(message).
func SerializeMessage(m Message) ([]byte, error) { return jsonv.Stringify(m) }

// IsJSONRPCRequest is isJSONRPCRequest: {jsonrpc: "2.0", id: string | safe integer, method:
// string, params?: object} with no other keys.
func IsJSONRPCRequest(v any) bool { return s.Valid(JSONRPCRequestSchema, v) }

// IsJSONRPCNotification is isJSONRPCNotification: {jsonrpc, method, params?}, no other keys.
func IsJSONRPCNotification(v any) bool { return s.Valid(JSONRPCNotificationSchema, v) }

// IsJSONRPCResultResponse is isJSONRPCResultResponse: {jsonrpc, id, result: object}, no other
// keys; result._meta, when present, must be an object.
func IsJSONRPCResultResponse(v any) bool { return s.Valid(JSONRPCResultResponseSchema, v) }

// IsJSONRPCErrorResponse is isJSONRPCErrorResponse: {jsonrpc, id?: string | safe integer,
// error: {code: safe integer, message: string, data?}}, no other top-level keys. A null id is
// NOT an error response.
func IsJSONRPCErrorResponse(v any) bool { return s.Valid(JSONRPCErrorResponseSchema, v) }

// IsJSONRPCResponse is isJSONRPCResponse.
func IsJSONRPCResponse(v any) bool { return IsJSONRPCResultResponse(v) || IsJSONRPCErrorResponse(v) }

// IsInitializeRequest is isInitializeRequest (non-strict: extra keys allowed, but params must be
// valid InitializeRequestParams).
func IsInitializeRequest(v any) bool { return s.Valid(initializeRequestSchema, v) }

// IsInitializedNotification is isInitializedNotification.
func IsInitializedNotification(v any) bool { return s.Valid(initializedNotificationSchema, v) }

// MessageID returns the message's id value (string or float64) and whether it is present.
func MessageID(m Message) (any, bool) {
	v, ok := m.Get("id")
	if !ok || jsonv.IsUndefined(v) {
		return nil, false
	}
	return v, true
}

// MessageMethod returns the method of a request or notification.
func MessageMethod(m Message) (string, bool) {
	v, ok := m.Get("method")
	if !ok {
		return "", false
	}
	str, ok := v.(string)
	return str, ok
}

// idNumber is Number(id), which Protocol uses to look up response and progress handlers (so a
// string id "1" answers request 1).
func idNumber(id any) (float64, bool) {
	switch t := id.(type) {
	case float64:
		return t, true
	case string:
		f := s.ToNumber(t)
		n, ok := f.(float64)
		return n, ok
	}
	return 0, false
}
