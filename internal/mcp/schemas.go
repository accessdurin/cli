package mcp

import (
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	s "github.com/accessdurin/cli/internal/mcp/schema"
)

// Neutral (core) schemas: @modelcontextprotocol/core 2.3.1 src/schemas.ts. These classify and
// parse JSON-RPC messages in the transports and predicates.

var (
	jsonValueSchema  s.Schema
	jsonObjectSchema s.Schema
)

func init() {
	jsonValueSchema = s.Lazy(func() s.Schema {
		return s.Union(s.String(), s.Number(), s.Boolean(), s.Null(), s.Record(jsonValueSchema), s.Array(jsonValueSchema))
	})
	jsonObjectSchema = s.Record(jsonValueSchema)
}

var requestIDSchema = s.Union(s.String(), s.Number().Int())

var requestMetaSchema = s.LooseObject(
	s.F("progressToken", s.Optional(s.Union(s.String(), s.Number().Int()))),
	s.F("io.modelcontextprotocol/related-task", s.Optional(s.Object(s.F("taskId", s.String())))),
)

var baseRequestParamsSchema = s.Object(s.F("_meta", s.Optional(requestMetaSchema)))

var iconSchema = s.Object(
	s.F("src", s.String()),
	s.F("mimeType", s.Optional(s.String())),
	s.F("sizes", s.Optional(s.Array(s.String()))),
	s.F("theme", s.Optional(s.Enum("light", "dark"))),
)

// implementationSchema is ImplementationSchema (name, title, icons, version, websiteUrl,
// description).
var implementationSchema = s.Object(
	s.F("name", s.String()),
	s.F("title", s.Optional(s.String())),
	s.F("icons", s.Optional(s.Array(iconSchema))),
	s.F("version", s.String()),
	s.F("websiteUrl", s.Optional(s.String())),
	s.F("description", s.Optional(s.String())),
)

var resultMetaObjectSchema = s.LooseObject(
	s.F("io.modelcontextprotocol/serverInfo", s.Catch(s.Optional(implementationSchema), s.Undefined)),
)

var resultSchema = s.LooseObject(s.F("_meta", s.Optional(resultMetaObjectSchema)))

var jsonrpcLiteral = s.Literal("2.0")

// JSONRPCRequestSchema and friends are the strict JSON-RPC envelope schemas.
var (
	JSONRPCRequestSchema = s.Object(
		s.F("jsonrpc", jsonrpcLiteral),
		s.F("id", requestIDSchema),
		s.F("method", s.String()),
		s.F("params", s.Optional(baseRequestParamsSchema.Loose())),
	).Strict()
	JSONRPCNotificationSchema = s.Object(
		s.F("jsonrpc", jsonrpcLiteral),
		s.F("method", s.String()),
		s.F("params", s.Optional(baseRequestParamsSchema.Loose())),
	).Strict()
	JSONRPCResultResponseSchema = s.Object(
		s.F("jsonrpc", jsonrpcLiteral),
		s.F("id", requestIDSchema),
		s.F("result", resultSchema),
	).Strict()
	JSONRPCErrorResponseSchema = s.Object(
		s.F("jsonrpc", jsonrpcLiteral),
		s.F("id", s.Optional(requestIDSchema)),
		s.F("error", s.Object(
			s.F("code", s.Number().Int()),
			s.F("message", s.String()),
			s.F("data", s.Optional(s.Unknown())),
		)),
	).Strict()
	JSONRPCMessageSchema = s.Union(JSONRPCRequestSchema, JSONRPCNotificationSchema, JSONRPCResultResponseSchema, JSONRPCErrorResponseSchema)
)

// ---- initialize (neutral), used by isInitializeRequest / isInitializedNotification ----

var formElicitationCapabilitySchema = s.Intersection(s.Object(s.F("applyDefaults", s.Optional(s.Boolean()))), jsonObjectSchemaRef())

func jsonObjectSchemaRef() s.Schema { return s.Lazy(func() s.Schema { return jsonObjectSchema }) }

var elicitationCapabilitySchema = s.Preprocess(func(v any) any {
	if o, ok := v.(*jsonv.Object); ok && o != nil && o.Len() == 0 {
		return jsonv.NewObject("form", jsonv.NewObject())
	}
	return v
}, s.Intersection(s.Object(
	s.F("form", s.Optional(formElicitationCapabilitySchema)),
	s.F("url", s.Optional(jsonObjectSchemaRef())),
), s.Optional(jsonObjectSchemaRef())))

var clientTasksCapabilitySchema = s.LooseObject(
	s.F("list", s.Optional(jsonObjectSchemaRef())),
	s.F("cancel", s.Optional(jsonObjectSchemaRef())),
	s.F("requests", s.Optional(s.LooseObject(
		s.F("sampling", s.Optional(s.LooseObject(s.F("createMessage", s.Optional(jsonObjectSchemaRef()))))),
		s.F("elicitation", s.Optional(s.LooseObject(s.F("create", s.Optional(jsonObjectSchemaRef()))))),
	))),
)

var clientCapabilitiesSchema = s.Object(
	s.F("experimental", s.Optional(s.Record(jsonObjectSchemaRef()))),
	s.F("sampling", s.Optional(s.Object(
		s.F("context", s.Optional(jsonObjectSchemaRef())),
		s.F("tools", s.Optional(jsonObjectSchemaRef())),
	))),
	s.F("elicitation", s.Optional(elicitationCapabilitySchema)),
	s.F("roots", s.Optional(s.Object(s.F("listChanged", s.Optional(s.Boolean()))))),
	s.F("tasks", s.Optional(clientTasksCapabilitySchema)),
	s.F("extensions", s.Optional(s.Record(jsonObjectSchemaRef()))),
)

var initializeRequestSchema = s.Object(
	s.F("method", s.Literal("initialize")),
	s.F("params", baseRequestParamsSchema.Extend(
		s.F("protocolVersion", s.String()),
		s.F("capabilities", clientCapabilitiesSchema),
		s.F("clientInfo", implementationSchema),
	)),
)

var initializedNotificationSchema = s.Object(
	s.F("method", s.Literal("notifications/initialized")),
	s.F("params", s.Optional(s.Object(s.F("_meta", s.Optional(requestMetaSchema))))),
)

// ---- 2025-11-25 wire schemas (rev2025-11-25/buildSchemas.ts), used to validate results ----

var wireRequestMeta = requestMetaSchema

var wireResult = s.LooseObject(s.F("_meta", s.Optional(wireRequestMeta)))

var wireImplementation = implementationSchema

var wireServerCapabilities = s.Object(
	s.F("experimental", s.Optional(s.Record(jsonObjectSchemaRef()))),
	s.F("logging", s.Optional(jsonObjectSchemaRef())),
	s.F("completions", s.Optional(jsonObjectSchemaRef())),
	s.F("prompts", s.Optional(s.Object(s.F("listChanged", s.Optional(s.Boolean()))))),
	s.F("resources", s.Optional(s.Object(
		s.F("subscribe", s.Optional(s.Boolean())),
		s.F("listChanged", s.Optional(s.Boolean())),
	))),
	s.F("tools", s.Optional(s.Object(s.F("listChanged", s.Optional(s.Boolean()))))),
	s.F("tasks", s.Optional(s.LooseObject(
		s.F("list", s.Optional(jsonObjectSchemaRef())),
		s.F("cancel", s.Optional(jsonObjectSchemaRef())),
		s.F("requests", s.Optional(s.LooseObject(
			s.F("tools", s.Optional(s.LooseObject(s.F("call", s.Optional(jsonObjectSchemaRef()))))),
		))),
	))),
	s.F("extensions", s.Optional(s.Record(jsonObjectSchemaRef()))),
)

// InitializeResultWireSchema is the 2025 InitializeResultSchema.
var InitializeResultWireSchema = wireResult.Extend(
	s.F("protocolVersion", s.String()),
	s.F("capabilities", wireServerCapabilities),
	s.F("serverInfo", wireImplementation),
	s.F("instructions", s.Optional(s.String())),
)

var wireAnnotations = s.Object(
	s.F("audience", s.Optional(s.Array(s.Enum("user", "assistant")))),
	s.F("priority", s.Optional(s.Number().Min(0).Max(1))),
	s.F("lastModified", s.Optional(s.ISODateTimeOffset())),
)

var wireResource = s.Object(
	s.F("name", s.String()),
	s.F("title", s.Optional(s.String())),
	s.F("icons", s.Optional(s.Array(iconSchema))),
	s.F("uri", s.String()),
	s.F("description", s.Optional(s.String())),
	s.F("mimeType", s.Optional(s.String())),
	s.F("size", s.Optional(s.Number())),
	s.F("annotations", s.Optional(wireAnnotations)),
	s.F("_meta", s.Optional(s.LooseObject())),
)

var wireMetaRecord = s.Optional(s.Record(s.Unknown()))

var wireResourceContents = s.Object(
	s.F("uri", s.String()),
	s.F("mimeType", s.Optional(s.String())),
	s.F("_meta", wireMetaRecord),
)

var wireContentBlock = s.Union(
	s.Object(s.F("type", s.Literal("text")), s.F("text", s.String()), s.F("annotations", s.Optional(wireAnnotations)), s.F("_meta", wireMetaRecord)),
	s.Object(s.F("type", s.Literal("image")), s.F("data", s.Base64()), s.F("mimeType", s.String()), s.F("annotations", s.Optional(wireAnnotations)), s.F("_meta", wireMetaRecord)),
	s.Object(s.F("type", s.Literal("audio")), s.F("data", s.Base64()), s.F("mimeType", s.String()), s.F("annotations", s.Optional(wireAnnotations)), s.F("_meta", wireMetaRecord)),
	wireResource.Extend(s.F("type", s.Literal("resource_link"))),
	s.Object(
		s.F("type", s.Literal("resource")),
		s.F("resource", s.Union(
			wireResourceContents.Extend(s.F("text", s.String())),
			wireResourceContents.Extend(s.F("blob", s.Base64())),
		)),
		s.F("annotations", s.Optional(wireAnnotations)),
		s.F("_meta", wireMetaRecord),
	),
)

var wireToolSchemaObject = func() *s.ObjectSchema {
	return s.Object(
		s.F("type", s.Literal("object")),
		s.F("properties", s.Optional(s.Record(jsonValueRef()))),
		s.F("required", s.Optional(s.Array(s.String()))),
	).Catchall(s.Unknown())
}

func jsonValueRef() s.Schema { return s.Lazy(func() s.Schema { return jsonValueSchema }) }

var wireTool = s.Object(
	s.F("name", s.String()),
	s.F("title", s.Optional(s.String())),
	s.F("icons", s.Optional(s.Array(iconSchema))),
	s.F("description", s.Optional(s.String())),
	s.F("inputSchema", wireToolSchemaObject()),
	s.F("outputSchema", s.Optional(wireToolSchemaObject())),
	s.F("annotations", s.Optional(s.Object(
		s.F("title", s.Optional(s.String())),
		s.F("readOnlyHint", s.Optional(s.Boolean())),
		s.F("destructiveHint", s.Optional(s.Boolean())),
		s.F("idempotentHint", s.Optional(s.Boolean())),
		s.F("openWorldHint", s.Optional(s.Boolean())),
	))),
	s.F("execution", s.Optional(s.Object(s.F("taskSupport", s.Optional(s.Enum("required", "optional", "forbidden")))))),
	s.F("_meta", wireMetaRecord),
)

// ListToolsResultWireSchema is the 2025 ListToolsResultSchema.
var ListToolsResultWireSchema = wireResult.Extend(
	s.F("nextCursor", s.Optional(s.String())),
	s.F("tools", s.Array(wireTool)),
)

var callToolResultSchema2025 = wireResult.Extend(
	s.F("content", s.Array(wireContentBlock)),
	s.F("structuredContent", s.Optional(s.Record(s.Unknown()))),
	s.F("isError", s.Optional(s.Boolean())),
)

// toolResultForeignFamilyKeys must never default into a {content: []} success.
var toolResultForeignFamilyKeys = []string{"task", "inputRequests", "requestState"}

// CallToolResultWireSchema is the 2025 CallToolResultWireSchema: a body without `content`
// gains `content: []` (appended last) unless it carries task/inputRequests/requestState, then
// it is parsed with CallToolResultSchema.
var CallToolResultWireSchema = s.Pipe(
	s.Transform(s.SuperRefine(s.Unknown(), func(v any) []s.Issue {
		o, ok := v.(*jsonv.Object)
		if !ok || o == nil || o.Has("content") {
			return nil
		}
		for _, k := range toolResultForeignFamilyKeys {
			if o.Has(k) {
				return s.CustomIssue("content is required when the body carries '" + k + "' — another result family cannot default into an empty tools/call success")
			}
		}
		return nil
	}), func(v any) (any, error) {
		o, ok := v.(*jsonv.Object)
		if !ok || o == nil || o.Has("content") {
			return v, nil
		}
		c := o.Clone()
		c.Set("content", []any{})
		return c, nil
	}),
	callToolResultSchema2025,
)

// EmptyResultWireSchema is the 2025 EmptyResultSchema (ping).
var EmptyResultWireSchema = wireResult.Strict()

var pingRequestWireSchema = s.Object(
	s.F("method", s.Literal("ping")),
	s.F("params", s.Optional(baseRequestParamsSchema.Loose())),
)

var progressNotificationWireSchema = s.Object(
	s.F("method", s.Literal("notifications/progress")),
	s.F("params", s.Object(
		s.F("_meta", s.Optional(requestMetaSchema)),
		s.F("progress", s.Number()),
		s.F("total", s.Optional(s.Number())),
		s.F("message", s.Optional(s.String())),
		s.F("progressToken", s.Union(s.String(), s.Number().Int())),
	)),
)
