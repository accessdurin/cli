package contracts

import (
	"regexp"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// McpReportLocalToolCallTool is the gateway tool the CLI reports local MCP calls through. It
// mirrors Durin's local tool-call contract (accessdurin/durin
// packages/shared/src/local-tool-calls.ts); the gateway validates the same rules.
const McpReportLocalToolCallTool = "durin_report_local_tool_call"

// LocalToolCallArgumentBytes bounds the JSON-encoded tool arguments in a report.
const LocalToolCallArgumentBytes = 16_384

var (
	singleLine       = regexp.MustCompile(`^\P{Cc}+$`)
	reportIdentifier = regexp.MustCompile(`^[\p{L}\p{N}._:/-]+$`)
	mcpName          = regexp.MustCompile(`^[\p{L}\p{N} ._:/@+-]+$`)
)

func label(max int) *zod.StringSchema {
	return zod.String().Trim().Min(1).Max(max).Regex(singleLine)
}

// ReportIdentifierSchema is a session, turn or call identifier as Durin accepts it.
var ReportIdentifierSchema = label(128).Regex(reportIdentifier)

// McpNameSchema is an MCP server or tool name as Durin accepts it.
func McpNameSchema(max int) *zod.StringSchema { return label(max).Regex(mcpName) }

// LocalToolCallSchema is one call an agent made to an MCP server on this computer.
var LocalToolCallSchema = zod.Object(
	zod.F("reportId", zod.UUID()),
	zod.F("reporter", zod.Literal(jsjson.String("cli"))),
	zod.F("agent", label(100)),
	zod.F("sessionId", zod.Nullable(ReportIdentifierSchema)),
	zod.F("turnId", zod.Nullable(ReportIdentifierSchema)),
	zod.F("toolCallId", zod.Nullable(ReportIdentifierSchema)),
	zod.F("mcpServer", McpNameSchema(100)),
	zod.F("tool", McpNameSchema(128)),
	zod.F("transport", zod.Enum("stdio", "loopback_http")),
	zod.F("outcome", zod.Enum("succeeded", "failed", "unknown")),
	zod.F("argumentsJson", zod.String().Max(LocalToolCallArgumentBytes)),
	zod.F("argumentsTruncated", zod.Boolean()),
	zod.F("observedAt", zod.ISODateTime()),
).Strict()
