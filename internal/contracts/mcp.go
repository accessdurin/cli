package contracts

import (
	"regexp"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// CliIdentifierSchema is cliIdentifierSchema: idempotency keys and approval IDs.
var CliIdentifierSchema = zod.String().Min(1).Max(128).Regex(regexp.MustCompile(`^[a-zA-Z0-9_:./-]+$`))

// ValidCliIdentifier reports whether cliIdentifierSchema accepts value.
func ValidCliIdentifier(value string) bool {
	return zod.Valid(CliIdentifierSchema, jsjson.String(value))
}

// CliToolNameSchema is cliToolNameSchema: agents interpolate tool names into shell commands,
// so only shell-inert names are accepted.
var CliToolNameSchema = zod.String().Regex(regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:/-]{0,127}$`))

// CliToolCallSchema is cliToolCallSchema.
var CliToolCallSchema = zod.Object(
	zod.F("name", CliToolNameSchema),
	zod.F("arguments", zod.Record(zod.JSON())),
	zod.F("idempotencyKey", CliIdentifierSchema),
	zod.F("approvalId", zod.Optional(CliIdentifierSchema)),
).Strict()

// ParseCliToolCall is cliToolCallSchema.safeParse on a call: ok is false when it is invalid.
// arguments may be any parsed JSON value; approvalID "" is absent.
func ParseCliToolCall(name *string, arguments jsjson.Value, idempotencyKey *string, approvalID string) (CliToolCall, bool) {
	input := jsjson.NewObject()
	input.Set("name", optionalString(name))
	input.Set("arguments", arguments)
	input.Set("idempotencyKey", optionalString(idempotencyKey))
	if approvalID != "" {
		input.Set("approvalId", jsjson.String(approvalID))
	}
	out, err := zod.SafeParse(CliToolCallSchema, input)
	if err != nil {
		return CliToolCall{}, false
	}
	o := out.(*jsjson.Object)
	call := CliToolCall{}
	call.Name, _ = o.GetString("name")
	call.Arguments, _ = o.GetObject("arguments")
	call.IdempotencyKey, _ = o.GetString("idempotencyKey")
	call.ApprovalID, _ = o.GetString("approvalId")
	return call, true
}

func optionalString(s *string) jsjson.Value {
	if s == nil {
		return nil
	}
	return jsjson.String(*s)
}

// CliMcpNegotiationSchema is cliMcpNegotiationSchema.
var CliMcpNegotiationSchema = zod.Object(
	zod.F("id", zod.Union(zod.String(), zod.Number())),
	zod.F("result", zod.Object(zod.F("protocolVersion", zod.String()))),
)

// McpEndpointSchema is mcpEndpointSchema (src/contracts/mcp-endpoint.ts).
var McpEndpointSchema = zod.DiscriminatedUnion("status",
	zod.Object(zod.F("status", zod.Literal(jsjson.String("ready"))), zod.F("url", URL())).Strict(),
	zod.Object(zod.F("status", zod.Literal(jsjson.String("needs_setup"))), zod.F("url", URL())).Strict(),
	zod.Object(zod.F("status", zod.Literal(jsjson.String("unconfigured"))), zod.F("url", zod.Null())).Strict(),
)

// McpCatalogLimits is MCP_CATALOG_LIMITS.
var McpCatalogLimits = struct{ Pages, Tools, PageBytes, TotalBytes int }{64, 10_000, 1_048_576, 8_388_608}

var (
	jsonObject     = zod.Record(zod.JSON())
	jsonSchemaType = zod.Union(zod.Boolean(), jsonObject)
)

// McpDiscoveredToolSchema is mcpDiscoveredToolSchema: untrusted upstream data, retained for
// review; annotations never authorize execution.
var McpDiscoveredToolSchema = zod.Object(
	zod.F("name", zod.String().Min(1).Max(128)),
	zod.F("title", zod.Optional(zod.String())),
	zod.F("description", zod.Optional(zod.String())),
	zod.F("inputSchema", zod.Object(zod.F("type", zod.Literal(jsjson.String("object")))).Catchall(zod.JSON())),
	zod.F("outputSchema", zod.Optional(jsonSchemaType)),
	zod.F("annotations", zod.Optional(jsonObject)),
	zod.F("_meta", zod.Optional(jsonObject)),
).Catchall(zod.JSON())

// McpToolsPageSchema is mcpToolsPageSchema.
var McpToolsPageSchema = zod.Object(
	zod.F("tools", zod.Array(McpDiscoveredToolSchema).Max(McpCatalogLimits.Tools)),
	zod.F("nextCursor", zod.Optional(zod.Nullable(zod.String().Max(16_384)))),
).Catchall(zod.JSON())

// McpToolsListSchema is mcpToolsListSchema: a whole catalog, without a cursor, with unique
// tool names and at most McpCatalogLimits.TotalBytes of JSON.
var McpToolsListSchema = McpToolsPageSchema.Extend(
	zod.F("nextCursor", zod.Optional(zod.Never())),
).Refine(func(o *jsjson.Object) bool {
	tools, _ := o.GetArray("tools")
	seen := map[string]bool{}
	for _, t := range tools {
		name, _ := t.(*jsjson.Object).GetString("name")
		seen[name] = true
	}
	return len(seen) == len(tools)
}, "Duplicate tool identity").Refine(func(o *jsjson.Object) bool {
	return Utf8Length(jsjson.Stringify(o)) <= McpCatalogLimits.TotalBytes
}, "Catalog exceeds limit")

// Utf8Length is new TextEncoder().encode(s).byteLength for a string in jsjson's WTF-8 form:
// a lone surrogate encodes as U+FFFD, which has the same three-byte length.
func Utf8Length(s string) int { return len(s) }

var (
	metadataAnnotations = zod.Optional(zod.Record(zod.JSON()))
	metadataMeta        = zod.Optional(zod.Record(zod.JSON()))
	resultURI           = zod.String().Max(2048)
	mimeType            = zod.String().Min(1).Max(128)
	base64              = zod.String().Max(65_536).Regex(regexp.MustCompile(`^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`))
)

func withMetadata(fields ...zod.Field) *zod.ObjectSchema {
	return zod.Object(append(fields, zod.F("annotations", metadataAnnotations), zod.F("_meta", metadataMeta))...)
}

func literal(s string) zod.Schema { return zod.Literal(jsjson.String(s)) }

var resultContent = zod.Union(
	withMetadata(zod.F("type", literal("text")), zod.F("text", zod.String().Max(16_384))),
	withMetadata(zod.F("type", literal("image")), zod.F("data", base64), zod.F("mimeType", mimeType)),
	withMetadata(zod.F("type", literal("audio")), zod.F("data", base64), zod.F("mimeType", mimeType)),
	withMetadata(
		zod.F("type", literal("resource_link")),
		zod.F("uri", resultURI),
		zod.F("name", zod.String()),
		zod.F("title", zod.Optional(zod.String())),
		zod.F("description", zod.Optional(zod.String())),
		zod.F("mimeType", zod.Optional(mimeType)),
		zod.F("size", zod.Optional(zod.Number().Nonnegative())),
		zod.F("icons", zod.Optional(zod.Array(zod.Record(zod.JSON())))),
	),
	withMetadata(
		zod.F("type", literal("resource")),
		zod.F("resource", zod.Union(
			zod.Object(zod.F("uri", resultURI), zod.F("mimeType", zod.Optional(mimeType)), zod.F("_meta", metadataMeta), zod.F("text", zod.String().Max(16_384))).Strict(),
			zod.Object(zod.F("uri", resultURI), zod.F("mimeType", zod.Optional(mimeType)), zod.F("_meta", metadataMeta), zod.F("blob", base64)).Strict(),
		)),
	),
)

// McpToolResultSchema is mcpToolResultSchema: protocol content stays inert; the broker
// separately gates uninspectable content.
var McpToolResultSchema = zod.Object(
	zod.F("content", zod.Array(resultContent).Max(128)),
	zod.F("structuredContent", zod.Optional(zod.JSON())),
	zod.F("isError", zod.Optional(zod.Boolean())),
	zod.F("_meta", metadataMeta),
).Refine(func(o *jsjson.Object) bool {
	return Utf8Length(jsjson.Stringify(o)) <= 65_536
}, "Result exceeds limit")

// IsTextMcpResult is isTextMcpResult for a parsed result.
func IsTextMcpResult(result McpToolResult) bool {
	content, _ := result.GetArray("content")
	for _, item := range content {
		o := item.(*jsjson.Object)
		kind, _ := o.GetString("type")
		if kind == "text" {
			continue
		}
		if resource, ok := o.GetObject("resource"); kind == "resource" && ok && resource.Has("text") {
			continue
		}
		return false
	}
	return true
}
