package app

import (
	"reflect"
	"testing"
)

// Outcomes recorded from Node 22's util.parseArgs with the CLI's options table.
func TestParseCliArgsMatchesNode(t *testing.T) {
	type outcome struct {
		code        string
		keys        []string
		values      map[string]string
		positionals []string
	}
	cases := []struct {
		args []string
		want outcome
	}{
		{[]string{"-hx"}, outcome{code: codeUnknownOption}},
		{[]string{"-h", "x"}, outcome{keys: []string{"help"}, values: map[string]string{"help": "true"}, positionals: []string{"x"}}},
		{[]string{"--dev=1"}, outcome{code: codeInvalidOptionValue}},
		{[]string{"--profile", "--dev"}, outcome{code: codeInvalidOptionValue}},
		{[]string{"--profile=--dev"}, outcome{keys: []string{"profile"}, values: map[string]string{"profile": "--dev"}, positionals: []string{}}},
		{[]string{"--profile", "-"}, outcome{keys: []string{"profile"}, values: map[string]string{"profile": "-"}, positionals: []string{}}},
		{[]string{"--config-dir=", "a"}, outcome{keys: []string{"config-dir"}, values: map[string]string{"config-dir": ""}, positionals: []string{"a"}}},
		{[]string{"--profile", "a", "--profile", "b", "x"}, outcome{keys: []string{"profile"}, values: map[string]string{"profile": "b"}, positionals: []string{"x"}}},
		{[]string{"--", "--help"}, outcome{keys: nil, values: map[string]string{}, positionals: []string{"--help"}}},
		{[]string{"-"}, outcome{keys: nil, values: map[string]string{}, positionals: []string{"-"}}},
		{[]string{"---x"}, outcome{code: codeUnknownOption}},
		{[]string{"--=x"}, outcome{code: codeUnknownOption}},
		{[]string{"-p"}, outcome{code: codeUnknownOption}},
		{[]string{"--help=no"}, outcome{code: codeInvalidOptionValue}},
		{[]string{"a", "--force", "b"}, outcome{keys: []string{"force"}, values: map[string]string{"force": "true"}, positionals: []string{"a", "b"}}},
		{[]string{"-hh"}, outcome{keys: []string{"help"}, values: map[string]string{"help": "true"}, positionals: []string{}}},
		{[]string{"--profile"}, outcome{code: codeInvalidOptionValue}},
		{[]string{"--nope", "--profile"}, outcome{code: codeUnknownOption}},
		{[]string{"--profile", "--nope"}, outcome{code: codeInvalidOptionValue}},
	}
	for _, c := range cases {
		values, positionals, err := parseCliArgs(c.args)
		var got outcome
		if err != nil {
			got.code = err.(*ArgumentError).Code
		} else {
			got = outcome{keys: values.Keys, values: map[string]string{}, positionals: positionals}
			for k, v := range values.strings {
				got.values[k] = v
			}
			for k := range values.flags {
				got.values[k] = "true"
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.args, got, c.want)
		}
	}
}

func TestResolvedOptionsCanonicalizeTheAppOrigin(t *testing.T) {
	options, err := ParseCliOptions([]string{"--app-url", "https://example.com.", "--config-dir", "relative/dir"})
	if err != nil {
		t.Fatal(err)
	}
	if options.AppOrigin != "https://example.com" || options.Directory != resolvePath("relative/dir") {
		t.Fatalf("got %q %q", options.AppOrigin, options.Directory)
	}
}
