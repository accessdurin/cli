// Generates fixtures.json for the jsjson differential tests from Node itself.
//
//   node internal/jsjson/testdata/gen.mjs > internal/jsjson/testdata/fixtures.json
//
// JSON.parse / JSON.stringify are V8's; terminalJson and canonicalJson are
// imported from the TypeScript CLI sources (Node 22 strips the types).
// The corpus is deterministic: random cases come from a seeded PRNG.
import { canonicalJson } from '/home/user/cli/src/canonical-json.ts';
import { terminalJson } from '/home/user/cli/src/terminal-text.ts';

let seed = 0x5eed1234;
const rand = () => {
  // mulberry32
  seed = (seed + 0x6d2b79f5) | 0;
  let t = seed;
  t = Math.imul(t ^ (t >>> 15), t | 1);
  t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const pick = (list) => list[Math.floor(rand() * list.length)];
const int = (n) => Math.floor(rand() * n);

const b64 = (bytes) => Buffer.from(bytes).toString('base64');
const units = (s) => Array.from({ length: s.length }, (_, i) => s.charCodeAt(i));

// ---------------------------------------------------------------- parse corpus
const parseCases = [];
const seen = new Set();
const outputs = (value) => {
  const out = { compact: JSON.stringify(value), canonical: canonicalJson(value) };
  const indent = JSON.stringify(value, null, 2);
  // Indented forms of the deep-nesting cases are quadratic; keep the fixture small.
  if (indent.length <= 20000) Object.assign(out, { indent, terminal: terminalJson(value) });
  return out;
};
const evaluate = (text) => {
  try {
    return { ok: true, ...outputs(JSON.parse(text)) };
  } catch (error) {
    if (!(error instanceof SyntaxError)) throw error;
    return { ok: false };
  }
};
/** Source given as bytes, decoded like Buffer#toString('utf8'). */
const addBytes = (bytes, note) => {
  const key = 'b' + b64(bytes);
  if (seen.has(key)) return;
  seen.add(key);
  parseCases.push({ note, b64: b64(bytes), ...evaluate(Buffer.from(bytes).toString('utf8')) });
};
const addText = (text, note) => {
  let wellFormed = text.isWellFormed();
  if (wellFormed) return addBytes(Buffer.from(text, 'utf8'), note);
  const key = 'u' + units(text).join(',');
  if (seen.has(key)) return;
  seen.add(key);
  parseCases.push({ note, u16: units(text), ...evaluate(text) });
};

const valid = [
  'null', 'true', 'false', '0', '-0', '1', '-1', '1.0', '1.5', '-1.5', '0.1', '1e3', '1E3', '1e+3',
  '1e-3', '1.25e2', '0e0', '-0.0', '0.0e-0', '1e400', '-1e400', '1e-400', '-1e-400', '5e-324',
  '2.4703282292062327e-324', '2.4703282292062328e-324', '4.9406564584124654e-324',
  '2.2250738585072014e-308', '2.2250738585072011e-308', '1.7976931348623157e308',
  '1.7976931348623158e308', '1.7976931348623159e308', '9007199254740991', '9007199254740992',
  '9007199254740993', '-9007199254740993', '18446744073709551615', '18446744073709551616',
  '123456789012345678901234567890', '0.30000000000000004', '100', '1e20', '1e21', '1e22',
  '123456789012345680000', '1234567890123456789012', '0.000001', '0.0000001', '1.5e-7',
  '0.00000123', '1.2345e-7', '4.35', '0.1e1', '10.0e-1', '1e-7', '1e-6', '-1e-7', '-1e21',
  '-123e-20', '1' + '0'.repeat(400), '0.' + '0'.repeat(400) + '1', '1.' + '9'.repeat(30),
  '""', '"a"', '" "', '"\\""', '"\\\\"', '"\\/"', '"\\b\\f\\n\\r\\t"', '"\\u0000"', '"\\u001f"',
  '"\\u007f"', '"\\u0080"', '"\\u009f"', '"\\u00a0"', '"\\u061c"', '"\\u200e\\u200f"',
  '"\\u202a\\u202b\\u202c\\u202d\\u202e"', '"\\u2066\\u2067\\u2068\\u2069"', '"\\u2028\\u2029"',
  '"\\uFEFF"', '"\\uffff"', '"\\uFFFE"', '"\\ud83d\\ude00"', '"\\uD83D\\uDE00"', '"\\ud800"',
  '"\\udfff"', '"\\udc00\\ud800"', '"\\ud800\\ud800"', '"\\ud800x"', '"x\\udc00"',
  '"\\ud800\\udc00\\udc00"', '"\\ud83d"', '"<>&\'"', '"\\u003c\\u003e\\u0026"', '"é"', '"😀"',
  '"\u{10ffff}"', '"\u{1F468}\u200d\u{1F469}"', '"\u0645\u0631\u062d\u0628\u0627"', '"日本語"',
  '"a\u0300"', '"\u007f\u0080\u0085\u009f"', '"\u061c\u200e\u200f\u202e\u2066\u2069"',
  '"\u2028\u2029"', '"\u00ad\u200b\u200c\u200d\u2060"', '"\\u000b\\u000c\\u000e\\u001b"',
  '[]', '{}', '[ ]', '{ }', ' [ 1 , 2 ] ', '\t\n\r [1]\r\n', '[[]]', '[{}]', '{"a":[]}',
  '{"a":{}}', '[1,"2",true,false,null,{"x":[1]}]', '{"a":1,"b":2}', '{"b":1,"a":2}',
  '{"a":1,"a":2}', '{"a":1,"b":2,"a":3}', '{"a":{"x":1},"a":[2]}', '{"":1}', '{"":1,"":2}',
  '{"1":1,"0":0}', '{"b":1,"2":2,"a":3,"1":4}', '{"10":1,"9":2,"1":3}',
  '{"01":1,"1":2,"1.0":3,"-1":4,"+1":5," 1":6}', '{"4294967294":1,"4294967295":2,"0":3}',
  '{"4294967296":1,"99999999999":2,"9007199254740993":3,"a":4}', '{"2":1,"1":2,"2":3}',
  '{"b":1,"a":2,"b":3,"1":4,"a":5}', '{"__proto__":1}', '{"__proto__":{"a":1},"b":2}',
  '{"constructor":1,"toString":2,"hasOwnProperty":3}', '{"\\ud800":1}',
  '{"\\ud800":1,"\\ud800":2}', '{"\\udc00":1,"\\ud800":2,"\uFFFF":3,"\u{10000}":4}',
  '{"é":1,"e":2,"z":3,"Z":4,"\u00e9":5}', '{"a":1,"A":2,"_":3,"-":4," ":5,"~":6}',
  '{"\u{1F600}":1,"\uFF01":2,"\uE000":3}', '{"\u202e":1}', '{"k\\u0000":1,"k":2}',
  '{"a":{"d":1,"c":{"f":1,"e":2}},"b":[{"z":1,"y":2}]}',
  '[{"b":1,"a":2},[{"d":3,"c":4}],"s",1.5]', '{"a":null,"b":[null]}',
  '{"x":1e400,"y":-1e400,"z":[1e400]}', '{"neg":-0,"arr":[-0,0,-0.0]}',
  '{"a":"\\u0000\\u0001\\u0002\\u0003\\u0004\\u0005\\u0006\\u0007\\u0008\\u0009\\u000a\\u000b\\u000c\\u000d\\u000e\\u000f\\u0010\\u0011\\u0012\\u0013\\u0014\\u0015\\u0016\\u0017\\u0018\\u0019\\u001a\\u001b\\u001c\\u001d\\u001e\\u001f"}',
  '[1,[2,[3,[4,[5,[6]]]]]]', '{"a":{"b":{"c":{"d":{"e":{}}}}}}',
  '"' + 'x'.repeat(1000) + '"', '[' + Array.from({ length: 200 }, (_, i) => i).join(',') + ']',
  '{' + Array.from({ length: 60 }, (_, i) => `"${(i * 7919) % 101}":${i}`).join(',') + '}',
  '{' + Array.from({ length: 40 }, (_, i) => `"k${(i * 31) % 17}":${i}`).join(',') + '}',
];
for (const text of valid) addText(text, 'valid');
addText('['.repeat(1000) + ']'.repeat(1000), 'deep array');
addText('{"a":'.repeat(500) + '1' + '}'.repeat(500), 'deep object');
addText('[{"b":' .repeat(200) + '[]' + '}]'.repeat(200), 'deep mixed');

const invalid = [
  '', ' ', '\n', 'nul', 'tru', 'fals', 'True', 'NULL', 'undefined', 'NaN', '-NaN', 'Infinity',
  '-Infinity', '+1', '.5', '1.', '-', '-.5', '01', '-01', '00', '1e', '1e+', '1E-', '0x10', '0b1',
  '1_000', '1.5.5', '--1', '1 2', '"a" "b"', '[1,]', '[,1]', '[,]', '[1,,2]', '{"a":1,}',
  '{,}', '{"a"}', '{"a":}', '{"a" 1}', '{a:1}', "{'a':1}", "'a'", '"a', '"\\"', '"\\x41"',
  '"\\u12"', '"\\u12G4"', '"\\U0041"', '"\\a"', '"\\\'"', '"\\0"', '"\\ "', '"\u0000"',
  '"\u0001"', '"\u001f"', '"\t"', '"\n"', '"\r"', '"a\nb"', '[1]]', '[[1]', '{"a":1}}',
  '{"a":1', '[', ']', '{', '}', ':', ',', '/* c */ 1', '1 // c', '# c\n1', '[1] x',
  '\u00a01', '1\u00a0', '\u000b1', '\u000c1', '\uFEFF1', '\u20281', '\u3000[]', '[1\u2028]',
  '{"a":1 "b":2}', '{"a",1}', '[1:2]', '{"a"::1}', '{1:1}', '{null:1}', '"\u2028', 'tRue',
  'nulll', 'truefalse', '[true false]', '"abc"x', '{"a":1}{"b":2}', '-Infinity1', '1e1.5',
  '1e01', '1.0e', '-0x0', '−1', '٣', '1\u0000', '\u00001',
];
for (const text of invalid) addText(text, 'invalid');

// Raw byte inputs: invalid UTF-8 inside and outside strings, BOM.
const byteInputs = [
  [0x22, 0xff, 0x22], [0x22, 0xc0, 0xaf, 0x22], [0x22, 0xe0, 0x80, 0xaf, 0x22],
  [0x22, 0xed, 0xa0, 0x80, 0x22], [0x22, 0xed, 0xa0, 0x80, 0xed, 0xb0, 0x80, 0x22],
  [0x22, 0xf4, 0x90, 0x80, 0x80, 0x22], [0x22, 0xf0, 0x9f, 0x98, 0x22], [0x22, 0xe2, 0x82, 0x22],
  [0x22, 0x80, 0x22], [0x22, 0xc3, 0x22], [0x22, 0xf5, 0x80, 0x22], [0x22, 0xef, 0xbf, 0xbd, 0x22],
  [0xef, 0xbb, 0xbf, 0x31], [0xff, 0x31], [0x31, 0xff], [0x5b, 0xc2, 0xa0, 0x5d],
  [0x7b, 0x22, 0xff, 0x22, 0x3a, 0x31, 0x2c, 0x22, 0xfe, 0x22, 0x3a, 0x32, 0x7d],
  [0x7b, 0x22, 0xed, 0xa0, 0x80, 0x22, 0x3a, 0x31, 0x7d], [0x22, 0xe2, 0x80, 0xae, 0x22],
  [0x22, 0xc2, 0x85, 0x22], [0x22, 0x7f, 0x22],
];
for (const bytes of byteInputs) addBytes(bytes, 'bytes');

// Lone surrogates present raw in the source text (a JS string, not bytes).
for (const text of [
  '"\ud800"', '"\udc00"', '"a\ud83d"', '"\ud83d\\ude00"', '"\\ud83d\ude00"', '{"\ud800":"\udfff"}',
  '["\ude00\ud83d"]', '\ud800', '[1,\udc00]',
])
  addText(text, 'raw surrogate');

// Mutation fuzzing around valid documents.
const fuzzSeeds = [
  '{"a":[1,2.5e3,"x\\u0041"],"b":{"c":null,"d":true}}', '[-0.5e-3,"\\ud83d\\ude00",{}]',
  '{"10":{"2":[false]},"z":"\\n"}', '"\\u00e9\\t"', '[1e21,123e-9,0]',
];
const fuzzAlphabet = [...'{}[]",:.-+eE0123456789 \t\n\\/ubfnrtx', '\u00a0', '\u2028', '\ud800', 'é', '😀', 'null', 'true', '\u0000', '\u001f'];
for (let i = 0; i < 2500; i++) {
  let text = pick(fuzzSeeds);
  const edits = 1 + int(3);
  for (let e = 0; e < edits; e++) {
    const at = int(text.length + 1);
    const op = int(3);
    if (op === 0) text = text.slice(0, at) + pick(fuzzAlphabet) + text.slice(at);
    else if (op === 1) text = text.slice(0, at) + text.slice(at + 1);
    else text = text.slice(0, at) + pick(fuzzAlphabet) + text.slice(at + 1);
  }
  addText(text, 'fuzz');
}
// Random valid strings mixing every interesting code unit class.
const unitPool = [
  0x00, 0x08, 0x09, 0x0a, 0x0c, 0x0d, 0x1f, 0x20, 0x22, 0x26, 0x27, 0x2f, 0x3c, 0x3e, 0x41, 0x5c,
  0x7e, 0x7f, 0x80, 0x85, 0x9f, 0xa0, 0xad, 0xe9, 0x61c, 0x2028, 0x2029, 0x200e, 0x200f, 0x202a,
  0x202e, 0x2066, 0x2069, 0x206a, 0x2065, 0xd7ff, 0xd800, 0xdbff, 0xdc00, 0xdfff, 0xe000, 0xfeff,
  0xfffd, 0xffff,
];
for (let i = 0; i < 400; i++) {
  const n = 1 + int(8);
  let s = '';
  for (let k = 0; k < n; k++) s += rand() < 0.15 ? '\u{1F600}' : String.fromCharCode(pick(unitPool));
  const doc = rand() < 0.5 ? JSON.stringify(s) : JSON.stringify({ [s]: s, x: [s] });
  addText(doc, 'random string');
}

// ---------------------------------------------------------------- numbers
const numbers = [];
const view = new DataView(new ArrayBuffer(8));
const addNumber = (x) => {
  view.setFloat64(0, x);
  const hex = view.getBigUint64(0).toString(16).padStart(16, '0');
  numbers.push({ bits: hex, str: String(x), json: JSON.stringify(x) });
};
for (const x of [
  0, -0, 1, -1, 0.1, 0.2, 0.3, 0.1 + 0.2, 1 / 3, 2 / 3, 100, 1e20, 1e21, 1e22, 123456789012345680000,
  1.5e-7, 1e-6, 1e-7, 0.000001, 0.0000012345, 123e-20, 5e-324, -5e-324, Number.MIN_VALUE,
  Number.MAX_VALUE, Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER + 2, 2 ** 53, 2 ** 64, 2 ** 70,
  2 ** -1074, 2 ** -1022, 2.2250738585072014e-308, 4.35, 0.5, 1.7976931348623157e308, NaN,
  Infinity, -Infinity, 9.999999999999999e20, 9.999999999999999e-7, 1e-323, 4294967295, 4294967296,
  -1e-7, 1.0000000000000002, 0.9999999999999999, 299792458, 6.02214076e23, 1.602176634e-19,
]) addNumber(x);
for (let e = -330; e <= 310; e++) addNumber(Number(`1e${e}`));
for (let e = -25; e <= 25; e++) addNumber(Number(`1.2345e${e}`));
for (let i = 0; i < 3000; i++) {
  view.setUint32(0, Math.floor(rand() * 2 ** 32));
  view.setUint32(4, Math.floor(rand() * 2 ** 32));
  const x = view.getFloat64(0);
  if (!Number.isNaN(x)) addNumber(x);
}
for (let i = 0; i < 1500; i++) {
  const digits = String(1 + int(9)) + Array.from({ length: int(17) }, () => int(10)).join('');
  addNumber(Number(`${rand() < 0.5 ? '-' : ''}${digits}e${int(60) - 35}`));
}
for (let i = 0; i < 300; i++) addNumber(Math.floor(rand() * 2 ** 53) * (rand() < 0.5 ? 1 : 2 ** int(30)));

// ---------------------------------------------------------------- UTF-8 decoding
const decode = [];
const addDecode = (bytes) => {
  decode.push({ b64: b64(bytes), u16: units(Buffer.from(bytes).toString('utf8')) });
};
for (const bytes of [
  [], [0x41], [0xef, 0xbb, 0xbf, 0x41], [0xef, 0xbb, 0xbf], [0x80], [0xbf], [0xc0], [0xc1, 0xbf],
  [0xc2], [0xc2, 0x80], [0xc2, 0x41], [0xdf, 0xbf], [0xe0, 0x80, 0x80], [0xe0, 0x9f, 0xbf],
  [0xe0, 0xa0, 0x80], [0xe0, 0xa0], [0xe0], [0xe1, 0x80], [0xed, 0x9f, 0xbf], [0xed, 0xa0, 0x80],
  [0xed, 0xbf, 0xbf], [0xed, 0xa0, 0xbd, 0xed, 0xb8, 0x80], [0xee, 0x80, 0x80], [0xef, 0xbf, 0xbf],
  [0xef, 0xbf, 0xbd], [0xf0, 0x80, 0x80, 0x80], [0xf0, 0x8f, 0xbf, 0xbf], [0xf0, 0x90, 0x80, 0x80],
  [0xf0, 0x90, 0x80], [0xf0, 0x90], [0xf0], [0xf4, 0x8f, 0xbf, 0xbf], [0xf4, 0x90, 0x80, 0x80],
  [0xf5, 0x80, 0x80, 0x80], [0xf8, 0x88, 0x80, 0x80, 0x80], [0xfc], [0xfe], [0xff],
  [0x41, 0xe2, 0x82, 0x41], [0xe2, 0x82, 0xac, 0xe2, 0x82], [0xf0, 0x9f, 0x98, 0x80, 0x80],
  [0xc0, 0xaf, 0xe0, 0x80, 0xaf], [0xf1, 0x80, 0x80, 0xe1, 0x80, 0xc2, 0x41],
]) addDecode(bytes);
const bytePool = [0x00, 0x41, 0x7f, 0x80, 0x8f, 0x90, 0x9f, 0xa0, 0xbf, 0xc0, 0xc2, 0xdf, 0xe0, 0xe1, 0xed, 0xee, 0xef, 0xf0, 0xf1, 0xf4, 0xf5, 0xff];
for (let i = 0; i < 1000; i++) addDecode(Array.from({ length: 1 + int(8) }, () => pick(bytePool)));

// ---------------------------------------------------------------- object building
const objects = [];
const keyPool = ['0', '1', '2', '10', '9', '01', '-1', '1.5', '4294967294', '4294967295', 'a', 'b', 'z', '', '__proto__', 'constructor', 'é', '\u{1F600}', 'B', '007', '1e3'];
for (let i = 0; i < 300; i++) {
  const ops = [];
  const o = Object.create(null);
  const n = 1 + int(12);
  for (let k = 0; k < n; k++) {
    const key = pick(keyPool);
    if (rand() < 0.2) {
      ops.push(['delete', key]);
      delete o[key];
    } else {
      const value = int(100);
      ops.push(['set', key, value]);
      o[key] = value;
    }
  }
  objects.push({ ops, compact: JSON.stringify(o), canonical: canonicalJson(o) });
}

const sections = { parse: parseCases, numbers, decode, objects };
process.stdout.write(
  '{\n' +
    Object.entries(sections)
      .map(([name, list]) => `"${name}": [\n${list.map((c) => JSON.stringify(c)).join(',\n')}\n]`)
      .join(',\n') +
    '\n}\n',
);
