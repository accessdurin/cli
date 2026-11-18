// Seeded random corpus for gen.mjs: JSON values rendered as JSONC in random
// styles (indentation, line endings, comments, trailing commas), random
// edits of them, and random doubles for number formatting.

// mulberry32: a small deterministic PRNG, so the fixtures are reproducible.
export function createRandom(seed) {
  let state = seed;
  const next = () => {
    state = (state + 0x6d2b79f5) | 0;
    let t = Math.imul(state ^ (state >>> 15), 1 | state);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  return {
    next,
    pick: (list) => list[Math.floor(next() * list.length)],
    chance: (p) => next() < p,
    int: (n) => Math.floor(next() * n),
  };
}

const WORDS = ['a', 'b', 'mcpServers', 'durin', 'list', 'hooks', 'x y', 'é', '😀', 'q"', 'b\\s'];
WORDS.push('10', '0', '__proto__');
const STRINGS = ['', 'v', 'https://example.com/a?b=c&d', 'line\nbreak', 'tab\there', 'ctl\u0001'];
STRINGS.push('\u2028', 'ü😀', '"', '\\', '/*not a comment*/', '//nope');
const NUMBERS = [0, -0, 1, -1, 1.5, 100, 1e21, 1e-7, 123456789.125, 2 ** 53, 0.1 + 0.2, 5e-324];
const MUTATION_CHARS = ['{', '}', '[', ']', ',', ':', '"', '/', '*', '\n', ' ', '1', 'x', '\\'];

const scalar = (r) =>
  r.pick([null, true, false, r.pick(STRINGS), r.pick(NUMBERS), r.pick(NUMBERS)]);

function randomObject(r, depth) {
  const o = {};
  // A "__proto__" key sets the prototype instead of adding a key, so
  // JSON.stringify (and therefore the fixture) omits it.
  for (let i = r.int(5); i > 0; i--) o[r.pick(WORDS)] = randomValue(r, depth - 1);
  return o;
}

export function randomValue(r, depth) {
  const x = r.next();
  if (depth <= 0 || x < 0.45) return scalar(r);
  if (x < 0.7) return Array.from({ length: r.int(4) }, () => randomValue(r, depth - 1));
  return randomObject(r, depth);
}

function comment(style) {
  if (!style.comments || !style.r.chance(0.3)) return '';
  return style.r.pick([' /* c */ ', '/*x*/', ` // line${style.eol}`]);
}

// Line break plus indentation for the given level ('' when minified).
const indentation = (style, level) =>
  style.indent === null ? '' : style.eol + style.indent.repeat(level);

function renderContainer(style, level, [open, close], parts) {
  const pad = indentation(style, level + 1);
  const end = indentation(style, level);
  if (!parts.length) return style.r.pick([open + close, `${open} ${close}`, open + end + close]);
  const items = parts.map((part) => comment(style) + pad + part());
  return open + items.join(',') + (style.trailing ? ',' : '') + comment(style) + end + close;
}

function renderValue(style, v, level) {
  const child = (x) => () => renderValue(style, x, level + 1);
  if (Array.isArray(v)) return renderContainer(style, level, '[]', v.map(child));
  if (v && typeof v === 'object') {
    const member = (k) => () => JSON.stringify(k) + style.colon + child(v[k])();
    return renderContainer(style, level, '{}', Object.keys(v).map(member));
  }
  return JSON.stringify(v);
}

// render returns value as JSONC text in a random style.
export function render(r, value) {
  const style = {
    r,
    indent: r.pick([null, '  ', '    ', '\t', ' ']),
    eol: r.pick(['\n', '\n', '\r\n', '\r']),
    comments: r.chance(0.4),
    trailing: r.chance(0.25),
    colon: r.pick([': ', ':', ' : ']),
  };
  const body = renderValue(style, value, 0);
  const header = style.comments && r.chance(0.5) ? `// header${style.eol}` : '';
  return header + body + (r.chance(0.5) ? style.eol : '');
}

const entries = (value) => {
  if (Array.isArray(value)) return value.map((v, i) => [i, v]);
  return value && typeof value === 'object' ? Object.entries(value) : [];
};

function collectPaths(value, prefix, out) {
  out.push(prefix);
  for (const [k, v] of entries(value)) collectPaths(v, [...prefix, k], out);
  return out;
}

// allPaths lists the path of every value in value, the root first.
export const allPaths = (value) => collectPaths(value, [], []);

function mutateOnce(r, text) {
  const at = r.int(text.length + 1);
  if (r.chance(0.5) && text.length) return text.slice(0, at) + text.slice(at + 1);
  return text.slice(0, at) + r.pick(MUTATION_CHARS) + text.slice(at);
}

// mutate deletes or inserts one or two structural characters.
export function mutate(r, text) {
  let out = text;
  for (let n = 1 + r.int(2); n > 0; n--) out = mutateOnce(r, out);
  return out;
}

const PATH_KINDS = [
  (_r, base) => base,
  (r, base) => [...base, r.pick(WORDS)],
  (_r, base, append) => [...base, append],
  (r, base) => [...base, r.pick(WORDS), r.chance(0.5) ? 0 : r.pick(WORDS)],
];

// randomPath extends base: unchanged, a new key, an append marker, or a new
// two-segment tail.
export const randomPath = (r, base, append) => PATH_KINDS[r.int(4)](r, base, append);

// randomNumbers returns n doubles: random bit patterns (finite ones) mixed
// with short decimals at random scales.
export function randomNumbers(r, n) {
  const out = [];
  const buf = new DataView(new ArrayBuffer(8));
  while (out.length < n) {
    buf.setUint32(0, Math.floor(r.next() * 2 ** 32));
    buf.setUint32(4, Math.floor(r.next() * 2 ** 32));
    const f = buf.getFloat64(0);
    if (Number.isFinite(f)) out.push(f);
    out.push(Math.floor(r.next() * 1e6) / 10 ** r.int(12));
  }
  return out;
}
