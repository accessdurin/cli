// Generates fixtures.json for the toml differential tests from smol-toml 1.9.1.
//
//   TZ=UTC node internal/toml/testdata/gen.mjs [toml-test-dir] > internal/toml/testdata/fixtures.json
//
// toml-test-dir is the tests/ directory of github.com/toml-lang/toml-test
// (for example `$(go env GOMODCACHE)/github.com/toml-lang/toml-test@v1.6.0/tests`);
// every .toml file below it joins the corpus with smol-toml's verdict.
// Random cases come from a seeded PRNG, so output is deterministic.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse, stringify } from '/home/user/cli/node_modules/smol-toml/dist/index.js';

if (process.env.TZ !== 'UTC') throw new Error('run with TZ=UTC');

let seed = 0x70e1;
const rand = () => {
  seed = (seed + 0x6d2b79f5) | 0;
  let t = seed;
  t = Math.imul(t ^ (t >>> 15), t | 1);
  t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const pick = (list) => list[Math.floor(rand() * list.length)];
const int = (n) => Math.floor(rand() * n);
const units = (s) => Array.from({ length: s.length }, (_, i) => s.charCodeAt(i));

/** Typed, order-preserving dump; mirrored by dump() in toml_test.go. */
const dump = (v) => {
  if (v instanceof Date) {
    let iso;
    try {
      iso = JSON.stringify(v.toISOString());
    } catch {
      iso = 'ERR';
    }
    return 'd' + iso + [v.isDate(), v.isTime(), v.isLocal(), v.isDateTime()].map(Number).join('');
  }
  if (Array.isArray(v)) return '[' + v.map(dump).join(',') + ']';
  if (typeof v === 'object') return '{' + Object.keys(v).map((k) => JSON.stringify(k) + ':' + dump(v[k])).join(',') + '}';
  if (typeof v === 'string') return JSON.stringify(v);
  if (typeof v === 'number') return Object.is(v, -0) ? '-0' : String(v);
  if (typeof v === 'boolean') return String(v);
  throw new Error('unexpected ' + typeof v);
};

// ---------------------------------------------------------------- parse corpus
const parseCases = [];
const seen = new Set();
const verdict = (text) => {
  try {
    const doc = parse(text);
    return { ok: true, dump: dump(doc), json: JSON.stringify(doc) };
  } catch (error) {
    if (error?.constructor?.name !== 'TomlError') return { ok: false, threw: String(error) };
    return { ok: false, line: error.line, column: error.column };
  }
};
const addBytes = (bytes, note) => {
  const b64 = Buffer.from(bytes).toString('base64');
  if (seen.has('b' + b64)) return;
  seen.add('b' + b64);
  parseCases.push({ note, b64, ...verdict(Buffer.from(bytes).toString('utf8')) });
};
const add = (text, note) => {
  if (text.isWellFormed()) return addBytes(Buffer.from(text, 'utf8'), note);
  const key = 'u' + units(text).join(',');
  if (seen.has(key)) return;
  seen.add(key);
  parseCases.push({ note, u16: units(text), ...verdict(text) });
};

const suite = process.argv[2];
if (suite) {
  const walk = (dir) => {
    for (const name of readdirSync(dir).sort()) {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) walk(path);
      else if (name.endsWith('.toml')) addBytes(readFileSync(path), 'toml-test ' + relative(suite, path));
    }
  };
  walk(suite);
}

const handwritten = [
  // What Codex configs and the CLI itself write.
  '',
  '\n',
  'model = "o3"\n\n[mcp_servers.durin]\nurl = "https://mcp.example.com/mcp"\n',
  '[mcp_servers.durin]\nurl = "https://x"\nenabled = false\n',
  '[mcp_servers]\ndurin = { url = "https://x", enabled = true }\n',
  'mcp_servers.durin.url = "https://x"\n',
  '[mcp_servers.durin-0123abcd]\ncommand = "/usr/bin/node"\nargs = [ "/opt/durin/cli.js", "mcp", "serve", "--profile", "0123abcd4567", "--config-dir", "/home/u/.config/durin" ]\n',
  '[mcp_servers."durin-0123abcd"]\ncommand = "/usr/bin/node"\n',
  "[mcp_servers.'durin']\nurl = 'https://x'\n",
  '[mcp_servers.durin]\nurl = "https://x"\n[mcp_servers.durin]\nurl = "https://y"\n',
  '[mcp_servers.durin]\nurl = "a"\nurl = "b"\n',
  '[mcp_servers]\n[mcp_servers]\n',
  'mcp_servers = 1\n[mcp_servers.durin]\n',
  '[mcp_servers.durin]\n[mcp_servers]\ndurin = {}\n',
  '[mcp_servers.durin.env]\nA = "1"\n[mcp_servers.durin]\nurl = "x"\n',
  '[a.b]\n[a]\n[a.b]\n',
  '[a]\nb.c = 1\n[a.b]\n',
  '[a]\nb.c = 1\n[a.b.d]\n',
  'a.b = 1\na.c = 2\n',
  'a.b = 1\na = 2\n',
  'a = {}\na.b = 1\n',
  'a = { b = 1 }\n[a.c]\n',
  '[[a]]\nb = 1\n[[a]]\nb = 2\n[a.c]\n',
  '[[a.b]]\n[a]\nc = 1\n',
  '[[a.b]]\n[a]\n[a]\n',
  '[[a]]\n[[a.b]]\nx = 1\n[[a.b]]\nx = 2\n[[a]]\n[[a.b]]\n',
  'a = [ { b = 1 } ]\n[[a]]\n',
  '[a]\n[[a]]\n',
  '[[a]]\n[a]\n',
  '[a.b.c]\nd = 1\n[a]\ne = 2\n[a.b]\nf = 3\n',
  '"" = 1\n',
  "'' = 1\n",
  '"a.b" = 1\na.b = 2\n',
  '"\\u0000" = 1\n',
  '"key" = 1\nkey = 2\n',
  '1 = 1\n2 = 2\n0 = 0\nb = 1\na = 1\n10 = 10\n',
  '[10]\n[2]\n[a]\n[1]\n',
  '__proto__ = 1\nconstructor = 2\n',
  '[__proto__]\nx = 1\n',
  'a = { __proto__ = 1 }\n',
  'a = 1 # comment\n# another\n',
  'a = 1 # comment with \x7f\n',
  'a = 1 # comment with \x01\n',
  'a = 1 # comment with \t tab\n',
  'a = 1\r\nb = 2\r\n',
  'a = 1\rb = 2\n',
  'a = 1\n\r\n',
  '\ufeffa = 1\n',
  'a = 1\ufeff\n',
  'a = 1',
  'a = 1 b = 2\n',
  'a = \n',
  'a\n',
  '= 1\n',
  'a b = 1\n',
  'a.b. = 1\n',
  '.a = 1\n',
  'a..b = 1\n',
  'a . b = 1\n',
  'a. "b" = 1\n',
  '"a"b = 1\n',
  'é = 1\n',
  '"é" = 1\n',
  'a-b_C9 = 1\n',
  '[ a . b ]\n',
  '[a b]\n',
  '[]\n',
  '[a]]\n',
  '[[a]\n',
  '[[a] ]\n',
  '[ [a]]\n',
  '[a\n',
  '["a\nb"]\n',
  "a = '''multi\nline'''\n",
  'a = """\nmulti\\\n   line\\\n\n  end"""\n',
  'a = """a\\ b"""\n',
  'a = """a\\   \n b"""\n',
  'a = """""""\n',
  'a = """"""""\n',
  'a = """ab""""\n',
  "a = ''''''\n",
  "a = '''a''''\n",
  "a = '''a'''''\n",
  "a = '''a''''''\n",
  'a = """a\r\nb"""\n',
  'a = """a\rb"""\n',
  'a = "a\\rb"\n',
  'a = "\\b\\t\\n\\f\\r\\e\\"\\\\"\n',
  'a = "\\x41\\u00e9\\U0001F600"\n',
  'a = "\\uD800"\n',
  'a = "\\U00110000"\n',
  'a = "\\UFFFFFFFF"\n',
  'a = "\\x4"\n',
  'a = "\\u12g4"\n',
  'a = "\\a"\n',
  'a = "\\ "\n',
  'a = "tab\there"\n',
  'a = "del\x7f"\n',
  'a = "nul\0"\n',
  "a = 'lit\\n'\n",
  "a = 'lit\x7f'\n",
  'a = "unterminated\n',
  "a = 'unterminated\n",
  'a = "\u202e\u2066bidi\u0085"\n',
  'a = "\ud83d\ude00"\n',
  "\"\\\"\" = '\"'\n",
  'a = true\nb = false\n',
  'a = tru\n',
  'a = truee\n',
  'a = True\n',
  'a = falsey\n',
  'a = 0\nb = -0\nc = +0\nd = 0.0\ne = -0.0\nf = +0.0\n',
  'a = 1_000\nb = 1__0\n',
  'a = 1_\n',
  'a = _1\n',
  'a = 01\n',
  'a = -01\n',
  'a = 0x1F\nb = 0o17\nc = 0b101\nd = 0xdead_beef\n',
  'a = 0X1F\n',
  'a = -0x1\n',
  'a = +0b1\n',
  'a = 0x\n',
  'a = 0x_1\n',
  'a = 0x1_\n',
  'a = 9007199254740991\nb = -9007199254740991\n',
  'a = 9007199254740992\n',
  'a = -9007199254740992\n',
  'a = 0x1FFFFFFFFFFFFF\n',
  'a = 0x20000000000000\n',
  'a = 99999999999999999999999\n',
  'a = 1.5\nb = -1.5e3\nc = 1e10\nd = 6.626e-34\ne = 1E+2\nf = 1_0.5_0\n',
  'a = 1.\n',
  'a = .5\n',
  'a = 1.e5\n',
  'a = 1e\n',
  'a = 1e_5\n',
  'a = 1_e5\n',
  'a = 1.5_\n',
  'a = 1e5.5\n',
  'a = 1e+-5\n',
  'a = 0e0\nb = 0.0e-3\nc = 0E5\n',
  'a = 1e400\nb = -1e400\nc = 1e-400\n',
  'a = 3.14159265358979323846\n',
  'a = inf\nb = +inf\nc = -inf\nd = nan\ne = +nan\nf = -nan\n',
  'a = Inf\n',
  'a = infinity\n',
  'a = NaN\n',
  'a = [ 1, 2.5, "x", true, [ ], { } ]\n',
  'a = [1,2,]\n',
  'a = [,]\n',
  'a = [1,,2]\n',
  'a = [\n  1, # one\n  2,\n]\n',
  'a = [1 2]\n',
  'a = [\n',
  'a = { b = 1, c = "x" }\n',
  'a = { b = 1, }\n',
  'a = {\n b = 1\n}\n',
  'a = { b = 1 c = 2 }\n',
  'a = { b = 1, b = 2 }\n',
  'a = { b.c = 1, b.d = 2 }\n',
  'a = { b = { c = 1 }, b.d = 2 }\n',
  'a = { b.c = 1, b = 2 }\n',
  'a = { b = [1], b.c = 2 }\n',
  'a = { b = 1979-05-27, b.c = 2 }\n',
  'a = {,}\n',
  'a = { b = 1\n',
  'a = { 1 = 1, 0 = 0, x = 2 }\n',
  'a = ' + '['.repeat(999) + ']'.repeat(999) + '\n',
  'a = ' + '['.repeat(1000) + ']'.repeat(1000) + '\n',
  'a = ' + '['.repeat(1001) + ']'.repeat(1001) + '\n',
  'a = ' + '{b='.repeat(1000) + '1' + '}'.repeat(1000) + '\n',
  'a = ' + '{b='.repeat(1001) + '1' + '}'.repeat(1001) + '\n',
  'a = 1979-05-27T07:32:00Z\nb = 1979-05-27T00:32:00-07:00\nc = 1979-05-27T00:32:00.999999-07:00\n',
  'a = 1979-05-27 07:32:00Z\n',
  'a = 1979-05-27T07:32:00\nb = 1979-05-27\nc = 07:32:00\nd = 00:32:00.999999\n',
  'a = [ 1979-05-27, 07:32:00, 1979-05-27T07:32:00Z ]\n',
  'a = { d = 1979-05-27 }\n',
  'a = 1979-05-27 # comment\n',
  'a = 1979-05-27T07:32:00Z # comment\n',
  'a = 1979-05-27x\n',
  'a = 07:32:00x\n',
  'a = 1979-05-27T07:32:00Zx\n',
  'a = 2024-02-29\nb = 2023-02-29\nc = 2023-02-31\nd = 2023-04-31\ne = 2023-02-32\n',
  'a = 0000-01-01T00:00:00Z\nb = 9999-12-31T23:59:59Z\n',
  'a = 9999-12-31T23:59:59-23:59\nb = 0000-01-01T00:00:00+23:59\n',
  'a = 1979-05-27T23:59:60Z\n',
  'a = 1979-05-27T24:00:00Z\n',
  'a = 1979-05-27T07:60:00Z\n',
  'a = 1979-05-27T07:32Z\n',
  'a = 1979-05-27T07:32\n',
  'a = 07:32\n',
  'a = 1979-05-27T07:32:00+0700\n',
  'a = 1979-05-27T07:32:00+07\n',
  'a = 1979-05-27T07:32:00 +07:00\n',
  'a = 1979-5-27\n',
  'a = 79-05-27\n',
  'a = 1979-05-27T\n',
  'a = 1979-05-27 \n',
  'a = 1979-05-27t07:32:00z\n',
  'a = 1979-05-27T07:32:00.Z\n',
  'a = 1979-05-27T07:32:00.123456789123Z\n',
  'a = 12:34:56.\n',
  'a = 24:00:00\n',
  'a = 23:59:59.9999\n',
  'a = 1:23:45\n',
  'a = 12:3:45\n',
  'a = 2:\n',
  'a = 12:\n',
  '[a]\nb = 1\n[c]\nd = [ { e = 1 } ]\n',
];
for (const text of handwritten) add(text, 'handwritten');
for (const text of ['a = "\ud800"\n', "a = '\udc00'\n", '"\ud800" = 1\n', '# \ud800\na = 1\n', '[\ud800]\n'])
  add(text, 'lone surrogate');
for (const bytes of [[0x61, 0x20, 0x3d, 0x20, 0x22, 0xff, 0x22, 0x0a], [0x23, 0xed, 0xa0, 0x80, 0x0a], [0xef, 0xbb, 0xbf, 0x61, 0x3d, 0x31]])
  addBytes(bytes, 'bytes');

// Date fuzzing: smol-toml only checks some positions and hands the rest to
// V8's Date parser (including its legacy fallback).
const datePool = [...'0123456789 \t+-.:()aAjJmMTtZzpPxX/,_#"\'=[]{}', '\n', '\r', '\u00a0', '\u2028', '\u3000', '\0', 'é'];
for (let i = 0; i < 2500; i++) {
  let d = pick(['1979-05-27', '1979-05-27T07:32:00Z', '1979-05-27 07:32:00', '1979-05-27T07:32:00.5-07:00', '07:32:00', '07:32', '2000-02-29T00:00', '1979-05-27T07:32:00+01:30']);
  const chars = [...d];
  const edits = 1 + int(3);
  for (let e = 0; e < edits; e++) {
    const at = int(chars.length + 1);
    if (rand() < 0.8) chars[at] = pick(datePool);
    else chars.splice(at, 0, pick(datePool));
  }
  d = chars.join('');
  add(`a = ${d}\n`, 'date fuzz');
  if (i % 5 === 0) add(`a = [${d}]\n`, 'date fuzz');
  if (i % 7 === 0) add(`a = {b=${d}}\n`, 'date fuzz');
}
// Exhaustive single-character variations of the unchecked date slots.
for (const at of [5, 6, 8]) for (const ch of datePool) {
  const chars = [...'1979-05-27'];
  chars[at] = ch;
  add(`a = ${chars.join('')}\n`, 'date slot');
}
for (const at of [11, 12, 14, 15, 17, 18]) for (const ch of datePool) {
  const chars = [...'1979-05-27T07:32:00Z'];
  chars[at] = ch;
  add(`a = ${chars.join('')}\n`, 'date-time slot');
}

// Mutation fuzzing around valid documents.
const seeds = [
  '[mcp_servers.durin]\nurl = "https://x"\nenabled = true\n',
  'a.b = [1, 2.5e3, "x\\u0041"]\n[t]\nc = { d = true, e = 0x1F }\n',
  '[[arr]]\nx = 1\n[[arr]]\ny = """m\nl"""\n',
  "k = 'lit' # c\nn = -1_000\nf = inf\nd = 1979-05-27\n",
];
const alphabet = [...'[]{}"\'=.,#\n \t\\01_xeE+-:TZabtfn', '\r\n', '"""', "'''", 'é', '\u0000', '\u007f', 'true', 'inf', 'nan'];
for (let i = 0; i < 3000; i++) {
  let text = pick(seeds);
  const edits = 1 + int(3);
  for (let e = 0; e < edits; e++) {
    const at = int(text.length + 1);
    const op = int(3);
    if (op === 0) text = text.slice(0, at) + pick(alphabet) + text.slice(at);
    else if (op === 1) text = text.slice(0, at) + text.slice(at + 1);
    else text = text.slice(0, at) + pick(alphabet) + text.slice(at + 1);
  }
  add(text, 'fuzz');
}

// ---------------------------------------------------------------- stringify corpus
// Inputs use a typed encoding (decoded by decodeInput in toml_test.go):
// {o:[[key, v]...]} object built by assignment, {a:[...]} array, {s} string,
// {su:[units]} string with lone surrogates, {n:"literal"} Number(literal),
// {b} boolean, {nul:1} null, {undef:1} undefined, {d:"toml literal"} TomlDate.
// A key is a string or an array of UTF-16 units.
const build = (e) => {
  if ('o' in e) {
    const o = {};
    for (const [k, v] of e.o) {
      const key = Array.isArray(k) ? String.fromCharCode(...k) : k;
      Object.defineProperty(o, key, { value: build(v), enumerable: true, writable: true, configurable: true });
    }
    return o;
  }
  if ('a' in e) return e.a.map(build);
  if ('s' in e) return e.s;
  if ('su' in e) return String.fromCharCode(...e.su);
  if ('n' in e) return Number(e.n);
  if ('b' in e) return e.b;
  if ('nul' in e) return null;
  if ('undef' in e) return undefined;
  if ('d' in e) return parse(`x = ${e.d}`).x;
  throw new Error('bad input');
};
const stringifyCases = [];
const addStringify = (input, note) => {
  let result;
  try {
    result = { ok: true, out: stringify(build(input)) };
  } catch (error) {
    result = { ok: false, error: String(error) };
  }
  stringifyCases.push({ note, input, ...result });
};
const S = (s) => ({ s });
const O = (...pairs) => ({ o: pairs });
const A = (...items) => ({ a: items });
const N = (n) => ({ n });
const B = (b) => ({ b });

// The exact shapes the CLI writes (bridge-config.ts).
const nodePaths = ['/usr/local/bin/node', '/home/u/.nvm/versions/node/v22.0.0/bin/node', 'C:\\Program Files\\nodejs\\node.exe', '/opt/my node/bin/node'];
const configDirs = ['/home/u/.config/durin', '/Users/Jane Doe/Library/Application Support/durin', '/tmp/"quoted"/dir', '/tmp/é日本/😀', '/tmp/tab\there', '/tmp/del\x7f/bidi\u202e'];
for (const nodePath of nodePaths)
  for (const configDirectory of configDirs) {
    const profileId = '0123abcd4567ef89';
    const name = `durin-${profileId.slice(0, 12)}`;
    const args = [`${configDirectory}/cli.js`, 'mcp', 'serve', '--profile', profileId, '--config-dir', configDirectory].map(S);
    addStringify(O(['mcp_servers', O([name, O(['command', S(nodePath)], ['args', A(...args)])])]), 'bridge args');
    addStringify(O(['mcp_servers', O([name, O(['type', S('stdio')], ['command', A(S(nodePath), ...args)], ['enabled', B(true)])])]), 'bridge command-array');
  }
addStringify(O(['mcpServers', O(['durin-0123abcd4567', O(['command', S('/n')], ['args', A()], ['disabled', B(false)])])]), 'bridge empty args');

const keys = ['durin', 'durin-0123abcd', 'a_b', 'A-Z_09', '', ' ', 'a.b', 'a b', 'é', '😀', '"', "'", '\\', '\n', '\x7f', '\u0000', '1', '01', '-', '_', 'mcp_servers', 'a\u202eb', '\u2028', 'true', '[x]', '#'];
for (const k of keys) {
  addStringify(O([k, S('v')]), 'key quoting');
  addStringify(O(['t', O([k, N('1')])]), 'table key quoting');
  addStringify(O([k, O(['x', B(false)])]), 'table header quoting');
}
addStringify(O([[0xd800], S('v')]), 'lone surrogate key');
addStringify(O(['t', O([[0xdc00, 0x61], S('v')])]), 'lone surrogate key');
addStringify(O([[0xd83d, 0xde00], S('v')]), 'surrogate pair key');

const strings = ['', 'plain', 'quote " inside', "apos ' inside", 'back\\slash', 'new\nline', 'cr\rret', 'tab\t', '\b\f', '\u0000\u0001\u001f', '\x7f', '\u0080\u009f', '\u00a0', '\u2028\u2029', '\u202e\u2066', '<>&', 'é', '日本語', '😀', '\ufeff', '\uffff', '"""', "'''", '#not a comment', '${x}'];
for (const s of strings) addStringify(O(['k', S(s)]), 'string escaping');
for (const su of [[0xd800], [0xdc00], [0x61, 0xd83d], [0xde00, 0xd83d], [0xd83d, 0xde00], [0xd800, 0xd800, 0x7f]])
  addStringify(O(['k', { su }], ['arr', A({ su })]), 'lone surrogate value');

const numbers = ['0', '-0', '1', '-1', '1.5', '0.1', '1e21', '-1e21', '1e20', '1e-7', '1.5e-7', '123456789012345680000', '9007199254740991', '9007199254740992', '-9007199254740992', '9007199254740993', '18446744073709551616', '1e300', '5e-324', '1.7976931348623157e308', 'NaN', 'Infinity', '-Infinity', '0.30000000000000004', '100', '4.35'];
for (const n of numbers) addStringify(O(['n', N(n)], ['in', A(N(n))], ['t', O(['x', N(n)])]), 'number');

addStringify(O(['b', B(true)], ['c', B(false)]), 'booleans');
addStringify(O(['a', A(S('x'), N('1'), B(true), A(), A(S('y')), O(), O(['z', N('2')]))]), 'mixed array');
addStringify(O(['a', A(O(['x', N('1')]), O(['y', N('2')]))]), 'array of tables');
addStringify(O(['a', A(O(), O())]), 'array of empty tables');
addStringify(O(['a', A(O(['x', N('1')]), S('s'))]), 'mixed objects array');
addStringify(O(['a', A(O(['b', A(O(['c', N('1')]), O())]), O())]), 'nested array of tables');
addStringify(O(['t', O(['u', O(['v', O()])])]), 'empty nested tables');
addStringify(O(['t', O(['x', N('1')], ['u', O(['y', N('2')])], ['z', N('3')])]), 'preamble after tables');
addStringify(O(['z', N('1')], ['2', N('2')], ['a', O(['1', N('1')])], ['1', N('3')]), 'integer keys first');
addStringify(O(['a', { nul: 1 }], ['b', { undef: 1 }], ['c', N('1')]), 'null and undefined members');
addStringify(O(['a', A(N('1'), { nul: 1 })]), 'null in array');
addStringify(O(['a', A({ undef: 1 })]), 'undefined in array');
addStringify(O(['a', A({ nul: 1 }, O())]), 'null in array of tables');
addStringify(O(['t', O(['x', { nul: 1 }])]), 'null in table');
addStringify(O(['a', A(O(['x', { nul: 1 }]))]), 'null in inline table');
addStringify(O(['a', A(O(['x', { undef: 1 }]))]), 'undefined in inline table');
addStringify(O(['a', A(A(O(['x', N('1')])))]), 'tables inside nested array');
addStringify(O(), 'empty');
addStringify(A(), 'array at top level');
addStringify(S('x'), 'string at top level');
addStringify({ nul: 1 }, 'null at top level');
for (const d of ['1979-05-27T07:32:00Z', '1979-05-27T00:32:00.999999-07:00', '1979-05-27T07:32:00', '1979-05-27', '07:32:00', '00:32:00.5', '1979-05-27t07:32:00z', '9999-12-31T23:59:59-23:59'])
  addStringify(O(['d', { d }], ['in', A({ d })], ['t', O(['x', { d }])]), 'date');
let deep = O(['x', N('1')]);
for (let i = 0; i < 999; i++) deep = O(['t', deep]);
addStringify(deep, 'deep tables 1000');
deep = O(['t', deep]);
addStringify(deep, 'deep tables 1001');
let deepArr = A(N('1'));
for (let i = 0; i < 998; i++) deepArr = A(deepArr);
addStringify(O(['a', deepArr]), 'deep array 999');
addStringify(O(['a', A(deepArr)]), 'deep array 1000');
let deepInline = O(['x', N('1')]);
for (let i = 0; i < 998; i++) deepInline = O(['y', deepInline]);
addStringify(O(['a', A(deepInline)]), 'deep inline');
addStringify(O(['a', A(O(['y', deepInline]))]), 'deep inline + 1');

// Random objects of the supported value kinds.
const randomValue = (depth) => {
  const r = rand();
  if (depth > 3 || r < 0.35) return S(pick(strings));
  if (r < 0.45) return N(pick(numbers));
  if (r < 0.55) return B(rand() < 0.5);
  if (r < 0.7) return A(...Array.from({ length: int(4) }, () => (rand() < 0.3 ? randomObject(depth + 1) : randomValue(depth + 1))));
  return randomObject(depth + 1);
};
const randomObject = (depth) => O(...Array.from({ length: int(5) }, () => [pick(keys.slice(0, 12)), randomValue(depth)]));
for (let i = 0; i < 400; i++) addStringify(randomObject(0), 'random');

// ---------------------------------------------------------------- Date.parse
// The V8 date parser port (ES5 ISO + legacy fallback), checked directly.
const dates = [];
const addDate = (s) => {
  const t = new Date(s).getTime();
  dates.push({ u16: units(s), t: Number.isNaN(t) ? null : t });
};
for (const s of [
  '1979-05-27', '1979-05-27T07:32:00Z', '1979-05-27T07:32:00.123456+05:30', '1979-05-27T07:32:00+0530',
  '+275760-09-13T00:00:00Z', '+275760-09-13T00:00:00.001Z', '-271821-04-20T00:00:00Z', '-271821-04-19T23:59:59Z',
  '-000000-01-01', '+002000-01-01', '2000-01-01T24:00', '2000-01-01T24:00:00.000Z', '2000-01-01T24:01',
  'Thu, 01 Jan 1970 00:00:00 GMT', 'Thu, 01 Jan 1970 00:00:00 GMT+0100', 'Jan 1 2000', '1 January 2000',
  'January 1, 2000 10:00 PM', '2000/01/02', '1/2/2000', '1/2/49', '1/2/50', '1/2/99', '12/31/1999 23:59:59',
  '2000-01-01 10:00 am', '2000-01-01 12:00 am', '2000-01-01 13:00 pm', 'Tue May 27 1979 07:32:00 GMT-0700 (PDT)',
  '1979-05-27 07:32 EST', '1979-05-27 07:32 PDT', '1979-05-27 07:32 UT', '1979-05-27 07:32 UTC+5',
  '1979-05-27 07:32 Z', '27 May 1979', 'May-27-1979', '1979 May 27', 'foo 2000', 'foo2000', '2000 foo',
  '(comment) 2000-01-01', '2000-01-01 (comment', '2000-01-01)', '2000-01-01 +', '2000-01-01T00:00:00+24:00',
  '2000-01-01T00:00:00.', '2000-01-01T00:00:00.5', '2000-01-01T00:00:00.123456789123Z', '20000101',
  '2000', '200', '2000-13', '2000-12', '2000-02-30', '2000-02-31', '2000-02-32', '10:00', '10:00:00 2000-01-01',
  '2000-01-01 10::', '2000-01-01 10:00:00.5', '2000-01-01 10:00:00:00', '2000-01-01 10:00 +05', '2000-01-01 10:00 -0530',
  '2000-01-01 10:00 +123456', '2000-01-01 10:00 GMT-8', '2000-01-01 10:00 GMT-08:00', '', ' ', '\u2028',
  '2000-01-01\u00002', '1979- 5-27', '1979-05- 7', '1979-0 -27', '1979--5-27', '1979-+5-27', '1979-.5-27',
  '1979-:5-27', '1979-5 -27', '1979-0a-27', '1979-ja-27', '1979-ma-27', '1979-pm-27', '1979-z5-27',
  '1979-t5-27', '1979-(5-27', '1979-)5-27', '1979-\u20285-27', '1979-\u30005-27', '1979-\u00a05-27',
]) addDate(s);
const tokenPool = ['2000', '1979', '05', '5', '27', '31', '32', '0', '00', '12', '13', '24', '59', '60', '999', '123456', '1234567890',
  '-', '+', ':', '.', '/', ',', ' ', 'T', 't', 'Z', 'z', 'am', 'pm', 'AM', 'Jan', 'may', 'December', 'GMT', 'utc', 'ut', 'est', 'PDT',
  'foo', 'x', '(', ')', '(c)', '\u00a0', '\n', '\u2028', '\u00e9', '\u0000'];
for (let i = 0; i < 3000; i++) {
  let s = '';
  const n = 1 + int(8);
  for (let k = 0; k < n; k++) s += pick(tokenPool);
  addDate(s);
}

const sections = { parse: parseCases, stringify: stringifyCases, dates };
process.stdout.write(
  '{\n' +
    Object.entries(sections)
      .map(([name, list]) => `"${name}": [\n${list.map((c) => JSON.stringify(c)).join(',\n')}\n]`)
      .join(',\n') +
    '\n}\n',
);
