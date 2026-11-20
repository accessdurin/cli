// Hand-written corpus for gen.mjs: documents shaped like the agent config
// files the CLI edits, edge cases of the upstream algorithms, the
// modifications the CLI performs, and the option sets to run them with.

const claude = `{
  "numStartups": 3,
  "mcpServers": {
    "other": {
      "type": "http",
      "url": "https://example.com/mcp"
    }
  },
  "projects": {}
}`;
const crlf = (s) => s.replace(/\n/g, '\r\n');

// deep(n) nests n objects under "a" keys on one line.
export const deep = (n) => '{"a":'.repeat(n) + '{}' + '}'.repeat(n);

export const docs = [
  '',
  '{}',
  '{}\n',
  '  {}  ',
  '{\n}',
  '{ }',
  '[]',
  '[\n]',
  'null',
  '"str"',
  '42',
  'true',
  '{"mcpServers": {}}',
  claude,
  claude + '\n',
  claude.replace(/^( +)/gm, (m) => m + m),
  claude.replace(/^( +)/gm, (m) => '\t'.repeat(m.length / 2)),
  crlf(claude) + '\r\n',
  claude.replace(/\n/g, '\r'),
  '{"a":1,"mcpServers":{"x":{"command":"y","args":["a","b"]}},"hooks":{"preToolUse":[]}}',
  `// Cursor settings
{
  // servers
  "mcpServers": {
    "x": { "url": "http://a" } // trailing
  },
  "editor.fontSize": 14 // font
}
`,
  '/* header */\n{\n  /* a */ "a": 1, /* b */\n  "b": [ /* empty */ ]\n}\n',
  '{\n  "a": 1,\n  "mcpServers": {\n    "x": {},\n  },\n}\n',
  '{\n  "hooks": {\n    "preToolUse": [\n      // first\n      { "command": "a" }, // c1\n      /* second */ { "command": "b" }\n    ]\n  }\n}',
  '{"hooks": {"preToolUse": []}, "list": [ ]}',
  '{\n  "list": [1, 2, 3],\n  "hooks": {\n    "preToolUse": [\n    ]\n  }\n}',
  '{\n  "名前": "値",\n  "emoji": "😀 \u2028 x",\n  "esc": "\\u00e9\\ud83d\\ude00\\ud800",\n  "list": ["ü", "😀"]\n}',
  '{\n  "a": {\n    "b": {\n      "c": {\n        "d": {}\n      }\n    }\n  }\n}',
  '{"quo\\"te": 1, "back\\\\slash": {}, "tab\\t": 2, "sl\\/ash": [3]}',
  '{"a": 1 "b": 2}',
  '{"a": }',
  '{',
  '{"mcpServers": {',
  '[1,,2]',
  '{"a":1}}',
  "{'a': 1}",
  '/* unterminated',
  '{"a": "unterminated',
  '{"a": "bad \\x escape", "b": "\\u12"}',
  '{"a": "raw\ttab"}',
  '// just a comment\n',
  '{"mcpServers": {"durin": {"type": "http", "url": "https://old.example"}}}',
  '{ "version": 1, "hooks": { "preToolUse": [ { "type": "command", "bash": "x" } ] } }',
  '{\n  "theme": "dark",\n  "mcpServers": {\n    "durin": {\n      "httpUrl": "https://mcp.example/x"\n    }\n  }\n}\n',
  '{\n\n  "a": 1,\n\n\n  "b": 2\n}',
  '{} {}',
  '{"a": [1, 2, 3]}',
  '{\n  "a": 1   \n  \n}',
  '\t  {\n\t  "a": 1\n\t  }',
  '\ufeff{"a": 1}',
  '{"n": 1.5e10, "m": -0, "big": 12345678901234567890, "small": 1e-7, "e": 1E+2, "neg": -12.5}',
  '{"b": 1, "10": 2, "2": 3, "a": 4, "01": 5, "-1": 6, "4294967294": 7, "4294967295": 8}',
  '{"mcpServers": {"a": 1}, "mcpServers": {"b": 2}}',
  '{"__proto__": {"x": 1}, "y": 2}',
  '{"__proto__": null, "__proto__": 1, "z": 3}',
  '{"__proto__": {"__proto__": null}, "__proto__": [1]}',
  '{"__proto__": 5, "__proto__": []}',
  '{"__proto__": [], "__proto__": 1}',
  '{"__proto__": {}, "__proto__": null, "__proto__": 2}',
  '{"__proto__": {"__proto__": {"__proto__": null}}, "__proto__": 3}',
  '[1, 2,]',
  '{"a": [1, 2,], "b": {"c": 1,},}',
  '{"a": 1, // c\n}',
  '{"a": tru, "b": nul, "c": 01, "d": 1., "e": 1e, "f": -, "g": .5}',
  '{"a" 1}',
  '{"a": 1, "a": 2, "b": [{"a": 3}, {"a": 4, "a": 5}]}',
  '{"a": 1, "b": 2, "a": {"c": 3}}',
  '\n\n\n{"a": 1}\n',
  '{"mcpServers": []}',
  '{"mcpServers": "str"}',
  '{"hooks": {"preToolUse": {"not": "array"}}}',
  '[\n  // only comment\n]',
  '{\n  // only comment\n}',
  '{"a":1}\n// trailing comment',
  '{"x": {"y": [ {"z": [ [ ] ] } ] } }',
  deep(30),
  '{\r\n\t"a": [\r\n\t\t1\r\n\t]\r\n}\r\n',
  '{"a":1,\n"b":2}',
  '  // lead\n  {\n    "a": 1\n  }',
  '{"a": "x"/*c*/,"b":/*d*/2}',
  '{\n  "a": 1 /* tail */\n}',
];

// Extra parse-only inputs: empty-ish content, surrogate escapes and numbers.
export const parseOnlyTexts = [
  ' ',
  '  // c\n',
  '"\\ud83d\\ude00 \\ud83d \\ude00 \\udc00\\ud800 \\ud800\\u0041 \\u0000"',
  '[1e400, -1e400, 1.7976931348623157e308, 5e-324, 2.2250738585072014e-308]',
  '[0.1, 0.000001, 0.0000001, 1e21, 1e20, 123e-20, 100, 1E5, -0.0, 0.5, 1.25e-10, 9007199254740993]',
  '[123456789012345680000, 1000000000000000000000, 4.35, 0.30000000000000004, 1e-6, 1.0e-6, 25e-8]',
];

// Path segment replaced by the current length of the array at the preceding
// path (0 when there is none), so the op appends.
export const APPEND = Symbol('append');

export const handler = {
  type: 'command',
  bash: 'durin hook --event preToolUse',
  powershell: 'durin.exe hook',
  timeoutSec: 30,
};
export const installServer = {
  path: ['mcpServers', 'durin'],
  value: { type: 'http', url: 'https://mcp.durin.dev/abc' },
};
export const installBridge = {
  path: ['mcpServers', 'durin-abc123def456'],
  value: {
    command: '/usr/local/bin/durin',
    args: ['bridge', '--profile', 'abc123def456'],
    env: {},
  },
};
export const setVersion = { path: ['version'], value: 1 };
export const appendHook = {
  path: ['hooks', 'preToolUse', APPEND],
  value: { hooks: [handler], matcher: '*' },
};
export const removeListItem = { path: ['list', 1], remove: true };

// Ops: {path, value} sets the value, {path, remove: true} removes it.
export const ops = [
  installServer,
  installBridge,
  setVersion,
  appendHook,
  { path: ['hooks', 'preToolUse', 0], value: handler },
  { path: ['list', APPEND], value: 7 },
  { path: ['list', 1], value: 'second' },
  { path: ['list', 10], value: false },
  { path: ['list', -1], value: { k: [] } },
  { path: ['numStartups'], value: 4 },
  { path: ['mcpServers'], value: {} },
  { path: ['a', 'b', 'c', 'd', 'e'], value: [1, 2.5, true, false, null, 'x', 1e21, 1e-7, 0.1] },
  { path: ['projects', '/home/u/proj', 'allowedTools', 0], value: 'Bash' },
  { path: [], value: { replaced: true } },
  { path: ['s'], value: 'q"uote \\ back\n\r\t\b\f \u0001\u001f\u007f \u2028\u2029 <>&\' é 😀' },
  { path: ['名前 😀'], value: '値' },
  { path: ['quo"te\\key', 'tab\tkey'], value: { nested: { deep: [{}] } } },
  { path: ['10'], value: { b: 1, 2: 2, a: 3, 1: 4 } },
  { path: ['__proto__'], value: { x: 1 } },
  { path: ['mcpServers', 'other'], remove: true },
  { path: ['numStartups'], remove: true },
  { path: ['a'], remove: true },
  { path: ['hooks', 'preToolUse', 0], remove: true },
  removeListItem,
  { path: ['list', 2], remove: true },
  { path: ['list', 5], remove: true },
  { path: ['list', -1], remove: true },
  { path: ['missing', 'deep'], remove: true },
];

export const tab = { insertSpaces: false, tabSize: 4 };
export const space2 = { insertSpaces: true, tabSize: 2 };

// The first entry is what the CLI passes.
export const modifyOptions = [
  { formattingOptions: space2 },
  { formattingOptions: tab },
  {},
  { formattingOptions: { insertSpaces: true, tabSize: 4, eol: '\r\n', insertFinalNewline: true } },
  { formattingOptions: { insertSpaces: true, tabSize: 0 } },
];

// The first entry is what the CLI passes.
export const parseOptions = [
  { allowTrailingComma: true },
  {},
  { disallowComments: true },
  { allowEmptyContent: true },
];

export const formatOptions = [
  space2,
  tab,
  { insertSpaces: true, tabSize: 2, keepLines: true },
  { insertSpaces: true, tabSize: 3, eol: '\r\n', insertFinalNewline: true },
  { insertSpaces: true, tabSize: 0 },
  { insertSpaces: false, tabSize: 2, keepLines: true, insertFinalNewline: true },
];
