package mcp

import (
	"github.com/accessdurin/cli/internal/jsjson"
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
// ParseMessage reproduces exactly that.
type Message = *jsjson.Object

// ParseMessage is JSONRPCMessageSchema.parse: it validates a JSON value as one of the four
// strict JSON-RPC message shapes (first match wins: request, notification, result, error) and
// returns zod's parsed output.
func ParseMessage(v jsjson.Value) (Message, error) {
	out, err := s.Parse(JSONRPCMessageSchema, v)
	if err != nil {
		return nil, err
	}
	return out.(*jsjson.Object), nil
}

// ParseMessageJSON parses JSON text (JSON.parse) and then ParseMessage.
func ParseMessageJSON(data []byte) (Message, error) {
	v, err := jsjson.Parse(data)
	if err != nil {
		return nil, err
	}
	return ParseMessage(v)
}

// SerializeMessage is JSON.stringify(message).
func SerializeMessage(m Message) ([]byte, error) { return []byte(jsjson.Stringify(m)), nil }

// IsJSONRPCRequest is isJSONRPCRequest: {jsonrpc: "2.0", id: string | safe integer, method:
// string, params?: object} with no other keys.
func IsJSONRPCRequest(v jsjson.Value) bool { return s.Valid(JSONRPCRequestSchema, v) }

// IsJSONRPCNotification is isJSONRPCNotification: {jsonrpc, method, params?}, no other keys.
func IsJSONRPCNotification(v jsjson.Value) bool { return s.Valid(JSONRPCNotificationSchema, v) }

// IsJSONRPCResultResponse is isJSONRPCResultResponse: {jsonrpc, id, result: object}, no other
// keys; result._meta, when present, must be an object.
func IsJSONRPCResultResponse(v jsjson.Value) bool { return s.Valid(JSONRPCResultResponseSchema, v) }

// IsJSONRPCErrorResponse is isJSONRPCErrorResponse: {jsonrpc, id?: string | safe integer,
// error: {code: safe integer, message: string, data?}}, no other top-level keys. A null id is
// NOT an error response.
func IsJSONRPCErrorResponse(v jsjson.Value) bool { return s.Valid(JSONRPCErrorResponseSchema, v) }

// IsJSONRPCResponse is isJSONRPCResponse.
func IsJSONRPCResponse(v jsjson.Value) bool {
	return IsJSONRPCResultResponse(v) || IsJSONRPCErrorResponse(v)
}

// IsInitializeRequest is isInitializeRequest (non-strict: extra keys allowed, but params must be
// valid InitializeRequestParams).
func IsInitializeRequest(v jsjson.Value) bool { return s.Valid(initializeRequestSchema, v) }

// IsInitializedNotification is isInitializedNotification.
func IsInitializedNotification(v jsjson.Value) bool { return s.Valid(initializedNotificationSchema, v) }

// MessageID returns the message's id value (jsjson.String or jsjson.Number) and whether it is
// present.
func MessageID(m Message) (jsjson.Value, bool) {
	v, ok := m.Get("id")
	if !ok || v == nil {
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
	str, ok := v.(jsjson.String)
	return string(str), ok
}

// path walks object properties; it returns (nil, false) when any step is missing or not an
// object.
func path(v jsjson.Value, keys ...string) (jsjson.Value, bool) {
	cur := v
	for _, k := range keys {
		o, ok := cur.(*jsjson.Object)
		if !ok || o == nil {
			return nil, false
		}
		if cur, ok = o.Get(k); !ok {
			return nil, false
		}
	}
	return cur, true
}

// idNumber is Number(id), which Protocol uses to look up response and progress handlers (so a
// string id "1" answers request 1).
func idNumber(id jsjson.Value) (float64, bool) {
	switch t := id.(type) {
	case jsjson.Number:
		return float64(t), true
	case jsjson.String:
		return s.ToNumber(t), true
	}
	return 0, false
}
