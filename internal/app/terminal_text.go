package app

import (
	"io"
	"regexp"

	"github.com/accessdurin/cli/internal/jsjson"
)

// ansi is Node 22's stripVTControlCharacters pattern (lib/internal/util/inspect.js).
var ansi = regexp.MustCompile(`[\x{1B}\x{9B}][\[\]()#;?]*` +
	`(?:(?:(?:(?:;[-a-zA-Z\d/#&.:=?%@~_]+)*` +
	`|[a-zA-Z\d]+(?:;[-a-zA-Z\d/#&.:=?%@~_]*)*)?` +
	`(?:\x{07}|\x{1B}\x{5C}|\x{9C}))` +
	`|(?:(?:\d{1,4}(?:;\d{0,4})*)?` +
	`[\dA-PR-TZcf-nq-uy=><~]))`)

var controlOrBidi = regexp.MustCompile(`[\p{Cc}\x{061c}\x{200e}\x{200f}\x{202a}-\x{202e}\x{2066}-\x{2069}]`)

// TerminalText displays server-supplied text inert: no escape sequences, controls, or bidi
// overrides.
func TerminalText(value string) string {
	return controlOrBidi.ReplaceAllString(ansi.ReplaceAllString(value, ""), "")
}

// PrintJSON is printJson (src/tool-commands.ts): terminal-safe JSON and a newline.
func PrintJSON(w io.Writer, value jsjson.Value) error {
	_, err := io.WriteString(w, jsjson.TerminalJSON(value)+"\n")
	return err
}

var controlOrBidiExceptLayout = regexp.MustCompile(`[\x00-\x08\x0B-\x1F\x7F-\x9F]|[\x{061c}\x{200e}\x{200f}\x{202a}-\x{202e}\x{2066}-\x{2069}]`)

// SanitizeConsole is what sanitizeConsole (src/terminal-text.ts) does to one console message:
// library diagnostics can quote server responses, so only line layout (\n, \t) survives.
func SanitizeConsole(message string) string {
	return controlOrBidiExceptLayout.ReplaceAllString(ansi.ReplaceAllString(message, ""), "")
}

// ConsoleWarn is console.warn through the sanitized console: each message is sanitized and
// written to w with a newline. The MCP SDK's warnings reach stderr this way.
func ConsoleWarn(w io.Writer) func(string) {
	return func(message string) { _, _ = io.WriteString(w, SanitizeConsole(message)+"\n") }
}
