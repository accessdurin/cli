package app

import (
	"path/filepath"
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/weburl"
)

// Help is the text durin --help prints.
const Help = `durin — Onboard and connect your organization to your preferred agent

Usage:
  durin                         Choose login or signup, onboard, and set up an agent
  durin login | signup          Start or resume interactive onboarding
  durin install                 Resume interactive agent onboarding
  durin verify                  Verify authenticated MCP access
  durin profiles list           Show saved organization profiles
  durin profiles use <id>       Change the terminal profile
  durin logout [--force]        Revoke and clear this profile’s MCP grant
  durin tools list              List authorized tools as JSON
  durin tools inspect <name>    Print a tool's native schema
  durin tools call <name> --input <file|-> --idempotency-key <key>
                                Invoke a tool; retain the key when retrying
  durin mcp serve               Serve the selected profile over stdio
  durin mcp endpoint            Print the personal URL for a direct MCP client
  durin hooks status            Show local checker and hook configuration status
  durin hooks run --harness <id> Run a configured local pre-tool checker (JSON stdin)

Options:
  --profile <id>                Select an explicit organization profile
  --config-dir <path>           Profile directory (default: ~/.config/durin)
  --approval-id <id>            Resume a tool call after approval
  --force                       logout: clear the local grant even if revocation fails
  --app-url <origin>            Durin app origin (default: https://app.getdurin.com)
  --dev                        Allow loopback hosts and HTTP for local development
  --harness <id>                hooks run: selected built-in or custom harness
  -h, --help                   Show this help

Agent selection happens during onboarding. Direct MCP clients authorize separately.
`

// ArgumentError is the TypeError Node's util.parseArgs throws; Code is its ERR_PARSE_ARGS_*
// code. It is an expected error: never reported to telemetry.
type ArgumentError struct {
	Code    string
	Message string
}

func (e *ArgumentError) Error() string { return e.Message }

// IsArgumentError marks the error as a command-line usage error for telemetry.
func (e *ArgumentError) IsArgumentError() bool { return true }

const (
	codeUnknownOption      = "ERR_PARSE_ARGS_UNKNOWN_OPTION"
	codeInvalidOptionValue = "ERR_PARSE_ARGS_INVALID_OPTION_VALUE"
)

type optionType int

const (
	booleanOption optionType = iota
	stringOption
)

// cliOptionTypes is parseCliArgs's options table; only "help" has a short name (-h).
var cliOptionTypes = map[string]optionType{
	"help":            booleanOption,
	"profile":         stringOption,
	"config-dir":      stringOption,
	"input":           stringOption,
	"idempotency-key": stringOption,
	"approval-id":     stringOption,
	"app-url":         stringOption,
	"dev":             booleanOption,
	"force":           booleanOption,
	"harness":         stringOption,
}

// CliValues are parseArgs's values: Keys lists the options given, in the order each was first
// stored (Object.keys(values)); a repeated option keeps its last value.
type CliValues struct {
	Keys    []string
	strings map[string]string
	flags   map[string]bool
}

// String returns a string option's value and whether it was given.
func (v CliValues) String(name string) (string, bool) { s, ok := v.strings[name]; return s, ok }

// Flag returns a boolean option's value (false when absent).
func (v CliValues) Flag(name string) bool { return v.flags[name] }

func (v *CliValues) store(name string, value *string) {
	if _, seen := v.strings[name]; !seen {
		if _, seen := v.flags[name]; !seen {
			v.Keys = append(v.Keys, name)
		}
	}
	if value == nil {
		v.flags[name] = true
	} else {
		v.strings[name] = *value
	}
}

// CliOptions is cliOptions's result (src/cli-options.ts).
type CliOptions struct {
	Values      CliValues
	Positionals []string
	AppOrigin   string
	Development bool
	Directory   string
}

type argToken struct {
	positional bool
	name, raw  string
	value      *string
	inline     bool
}

func isLoneLongOption(arg string) bool {
	return len(arg) > 2 && strings.HasPrefix(arg, "--") && !strings.Contains(arg[3:], "=")
}

func isLongOptionAndValue(arg string) bool {
	return len(arg) > 2 && strings.HasPrefix(arg, "--") && strings.Contains(arg[3:], "=")
}

// shortName maps a short option to its long name, or to itself when none has it.
func shortName(short string) string {
	if short == "h" {
		return "help"
	}
	return short
}

func typeOf(name string) (optionType, bool) { t, ok := cliOptionTypes[name]; return t, ok }

// argsToTokens is Node's argsToTokens (lib/internal/util/parse_args/parse_args.js) for this
// options table. Strings are JavaScript strings (UTF-16), so short options are taken by code
// unit just as charAt does.
func argsToTokens(args []string) []argToken {
	var tokens []argToken
	remaining := append([]string(nil), args...)
	for len(remaining) > 0 {
		arg := remaining[0]
		remaining = remaining[1:]
		units := jsjson.ToUTF16(arg)
		charAt := func(i int) string { return jsjson.FromUTF16(units[i : i+1]) }
		sliceFrom := func(i int) string { return jsjson.FromUTF16(units[i:]) }
		if arg == "--" {
			for _, rest := range remaining {
				tokens = append(tokens, argToken{positional: true, value: ptr(rest)})
			}
			break
		}
		stringNext := func(name string) (*string, bool) {
			if t, ok := typeOf(name); ok && t == stringOption && len(remaining) > 0 {
				value := remaining[0]
				remaining = remaining[1:]
				return &value, false
			}
			return nil, false
		}
		switch {
		case len(units) == 2 && units[0] == '-' && units[1] != '-': // -f
			name := shortName(charAt(1))
			value, inline := stringNext(name)
			tokens = append(tokens, argToken{name: name, raw: arg, value: value, inline: inline})
		case len(units) > 2 && units[0] == '-' && units[1] != '-' && !isStringShort(charAt(1)): // -abc
			var expanded []string
			for i := 1; i < len(units); i++ {
				short := charAt(i)
				if !isStringShort(short) || i == len(units)-1 {
					expanded = append(expanded, "-"+short)
				} else {
					expanded = append(expanded, "-"+sliceFrom(i))
					break
				}
			}
			remaining = append(expanded, remaining...)
		case len(units) > 2 && units[0] == '-' && units[1] != '-': // -fVALUE
			short := charAt(1)
			tokens = append(tokens, argToken{name: shortName(short), raw: "-" + short, value: ptr(sliceFrom(2)), inline: true})
		case isLoneLongOption(arg): // --foo [value]
			name := arg[2:]
			value, inline := stringNext(name)
			tokens = append(tokens, argToken{name: name, raw: arg, value: value, inline: inline})
		case isLongOptionAndValue(arg): // --foo=value
			eq := strings.Index(arg, "=")
			name := arg[2:eq]
			tokens = append(tokens, argToken{name: name, raw: "--" + name, value: ptr(arg[eq+1:]), inline: true})
		default:
			tokens = append(tokens, argToken{positional: true, value: ptr(arg)})
		}
	}
	return tokens
}

// isStringShort reports whether a short option names a string option; none does here.
func isStringShort(short string) bool {
	t, ok := typeOf(shortName(short))
	return ok && t == stringOption
}

func ptr(s string) *string { return &s }

// parseCliArgs is util.parseArgs in strict mode with positionals allowed.
func parseCliArgs(args []string) (CliValues, []string, error) {
	values := CliValues{strings: map[string]string{}, flags: map[string]bool{}}
	positionals := []string{}
	for _, token := range argsToTokens(args) {
		if token.positional {
			positionals = append(positionals, *token.value)
			continue
		}
		kind, known := typeOf(token.name)
		if !known {
			return values, nil, &ArgumentError{Code: codeUnknownOption, Message: "Unknown option '" + token.raw + "'"}
		}
		shortAndLong := "--" + token.name
		if token.name == "help" {
			shortAndLong = "-h, --help"
		}
		if kind == stringOption && token.value == nil {
			return values, nil, &ArgumentError{Code: codeInvalidOptionValue, Message: "Option '" + shortAndLong + " <value>' argument missing"}
		}
		if kind == booleanOption && token.value != nil {
			return values, nil, &ArgumentError{Code: codeInvalidOptionValue, Message: "Option '" + shortAndLong + "' does not take an argument"}
		}
		if !token.inline && token.value != nil && jsjson.UTF16Len(*token.value) > 1 && strings.HasPrefix(*token.value, "-") {
			return values, nil, &ArgumentError{Code: codeInvalidOptionValue, Message: "Option '" + token.raw + "' argument is ambiguous."}
		}
		values.store(token.name, token.value)
	}
	return values, positionals, nil
}

// ParseCliOptions is cliOptions(args): parsed options with the app origin and configuration
// directory resolved. args are decoded as Node decodes process.argv (invalid UTF-8 becomes
// U+FFFD).
func ParseCliOptions(args []string) (*CliOptions, error) {
	decoded := make([]string, len(args))
	for i, a := range args {
		decoded[i] = jsjson.DecodeUTF8([]byte(a))
	}
	values, positionals, err := parseCliArgs(decoded)
	if err != nil {
		return nil, err
	}
	return resolvedOptions(values, positionals)
}

func resolvedOptions(values CliValues, positionals []string) (*CliOptions, error) {
	appOrigin, ok := values.String("app-url")
	if !ok {
		appOrigin = "https://app.getdurin.com"
	}
	development := values.Flag("dev")
	if !contracts.SafeCliURL(appOrigin, development) {
		return nil, clierror.New("Use an HTTPS app origin, or --dev with loopback HTTP.")
	}
	if u, err := weburl.Parse(appOrigin); err != nil || u.Origin() != appOrigin {
		return nil, clierror.New("--app-url must be an origin without a path.")
	}
	origin, err := canonicalOrigin(appOrigin)
	if err != nil {
		return nil, err
	}
	directory, given := values.String("config-dir")
	if !given {
		directory = filepath.Join(Homedir(), ".config", "durin")
	}
	return &CliOptions{
		Values:      values,
		Positionals: positionals,
		AppOrigin:   origin,
		Development: development,
		Directory:   resolvePath(directory),
	}, nil
}

// canonicalOrigin is the origin without one trailing dot, as the app's own /auth/cli/config
// names it. This is the only place a dot is stripped; a host that still ends in one names no
// durin app.
func canonicalOrigin(origin string) (string, error) {
	u, err := weburl.Parse(origin)
	if err != nil {
		return "", err
	}
	u.SetHostname(strings.TrimSuffix(u.Hostname(), "."))
	if strings.HasSuffix(u.Hostname(), ".") {
		original, _ := weburl.Parse(origin)
		return "", clierror.New("--app-url " + original.Host() + " ends in more than one dot, so it names no durin app.")
	}
	return u.Origin(), nil
}
