package app

import (
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

const endpointProblem = "Use the HTTPS personal /mcp/u/... endpoint copied from Durin, without credentials or query parameters."

var personalPath = regexp.MustCompile(`^/mcp/u/[a-f0-9]{64}$`)

// EndpointSchema is endpointSchema (src/endpoint.ts): a personal HTTPS MCP endpoint exactly as
// Durin issues it.
var EndpointSchema = zod.String().Max(2048).Refine(func(value string) bool {
	u, err := weburl.Parse(value)
	if err != nil {
		return false
	}
	return u.Protocol() == "https:" && u.Username() == "" && u.Password() == "" && u.Search() == "" &&
		u.Hash() == "" && personalPath.MatchString(u.Pathname()) && !strings.ContainsAny(value, "\r\n\t") &&
		value == u.Href()
}, endpointProblem)

// ValidateEndpoint is validateEndpoint: "" when value is a valid endpoint, else the problem.
func ValidateEndpoint(value string) string {
	if value == "" {
		return "Endpoint URL is required."
	}
	if zod.Valid(EndpointSchema, jsjson.String(value)) {
		return ""
	}
	return endpointProblem
}
