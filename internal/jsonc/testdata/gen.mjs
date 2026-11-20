// Generates the differential fixtures for internal/jsonc from the upstream
// npm package jsonc-parser 3.3.1 (the version the TypeScript CLI ships).
//
//   node internal/jsonc/testdata/gen.mjs
//
// Writes parse.jsonl, format.jsonl and modify.jsonl next to this script. All
// offsets and lengths are converted from UTF-16 code units to UTF-8 byte
// offsets, which is what the Go port reports. Values handed to modify are
// stored as JSON text (JSON.stringify output) and decoded by the Go test with
// jsonc.Parse, so key order and number formatting round-trip exactly.
import { createRequire } from 'node:module';
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import * as corpus from './gen-corpus.mjs';
import * as random from './gen-random.mjs';

// The fixtures must come from the exact package the TypeScript CLI ships,
// installed in the main checkout (worktrees have no node_modules).
const UPSTREAM = '/home/user/cli/node_modules/jsonc-parser';
const require = createRequire(import.meta.url);
const jsonc = require(UPSTREAM);
const pkg = require(join(UPSTREAM, 'package.json'));
if (pkg.version !== '3.3.1') throw new Error(`expected jsonc-parser 3.3.1, found ${pkg.version}`);

const here = dirname(fileURLToPath(import.meta.url));
const { APPEND, docs, modifyOptions, parseOptions, formatOptions } = corpus;

// ---------------------------------------------------------------- offsets

// UTF-16 offset -> UTF-8 byte offset. Offsets past the end (an unterminated
// block comment overshoots by one) keep their overshoot.
const byteOffset = (text, off) =>
  Buffer.byteLength(text.slice(0, Math.max(0, Math.min(off, text.length))), 'utf8') +
  Math.max(0, off - text.length);
const byteSpan = (text, off, len) => {
  const start = byteOffset(text, off);
  return { offset: start, length: byteOffset(text, off + len) - start };
};
const edits = (text, list) =>
  list.map((e) => Object.assign(byteSpan(text, e.offset, e.length), { content: e.content }));

// ---------------------------------------------------------------- modify

const modifyDocs = [];
const docIndex = new Map();
const docId = (text) => {
  if (!docIndex.has(text)) docIndex.set(text, modifyDocs.push(text) - 1);
  return docIndex.get(text);
};

const arrayLength = (text, path) => {
  const node = jsonc.findNodeAtLocation(jsonc.parseTree(text), path);
  return node?.type === 'array' ? node.children.length : 0;
};
const resolvePath = (text, path) =>
  path.map((seg, i) => (seg === APPEND ? arrayLength(text, path.slice(0, i)) : seg));

function step(state, op, options) {
  const path = resolvePath(state.text, op.path);
  state.record.ops.push(
    op.remove ? { path, remove: true } : { path, value: JSON.stringify(op.value) },
  );
  const list = jsonc.modify(state.text, path, op.remove ? undefined : op.value, options);
  state.record.steps.push(edits(state.text, list));
  state.text = jsonc.applyEdits(state.text, list);
}

function runOps(text, opList, options) {
  const record = { doc: docId(text), options, ops: [], steps: [] };
  const state = { record, text };
  try {
    for (const op of opList) step(state, op, options);
    // Single-step results follow from the exact edits; keep the text only
    // for sequences.
    if (opList.length > 1) record.result = state.text;
  } catch (e) {
    record.error = { kind: e.constructor.name, message: e.message };
  }
  return record;
}

// Error cases are capped per distinct message so the fixture stays small.
const modifyCases = [];
const errorCounts = new Map();
const countError = ({ kind, message }) => {
  const key = `${kind}: ${message}`;
  errorCounts.set(key, (errorCounts.get(key) ?? 0) + 1);
  return errorCounts.get(key);
};
const push = (record) => {
  if (record.error && countError(record.error) > 12) return;
  modifyCases.push(record);
};

const [cliOptions, tabOptions] = modifyOptions;
const docCases = (doc) => [
  ...corpus.ops.map((op) => [doc, [op], cliOptions]),
  ...modifyOptions
    .slice(1)
    .flatMap((options) =>
      [corpus.installServer, corpus.appendHook].map((op) => [doc, [op], options]),
    ),
  [doc, [corpus.removeListItem], tabOptions],
  // CLI-style sequences: hooks config (version, then append a handler), and
  // server + bridge install followed by re-installing on its own output.
  [
    doc,
    [
      corpus.setVersion,
      { path: ['hooks', 'preToolUse', APPEND], value: { hooks: [corpus.handler] } },
    ],
    cliOptions,
  ],
  [doc, [corpus.installServer, corpus.installBridge, corpus.installServer], cliOptions],
];
for (const doc of docs) for (const args of docCases(doc)) push(runOps(...args));

// Indentation exactly hitting upstream's 200-entry cache (tabSize 2 at
// depth 100, tabs at depth 200) throws a TypeError upstream; 99 and 101
// levels are fine.
for (const depth of [99, 100, 101]) {
  const path = Array.from({ length: depth }, (_, i) => `k${i}`);
  modifyCases.push(runOps('{}', [{ path, value: 1 }], cliOptions));
}
modifyCases.push(
  runOps(corpus.deep(99), [{ path: [...Array(99).fill('a'), 'z'], value: 1 }], cliOptions),
);
modifyCases.push(runOps('{}', [{ path: Array(200).fill(0), value: 'x' }], tabOptions));

// ---------------------------------------------------------------- random

const r = random.createRandom(0x6a736f6e);
// A random object usually gets an "mcpServers" key, like agent configs.
const randomRoot = (value) =>
  r.chance(0.85)
    ? { ...(r.chance(0.5) ? value : {}), mcpServers: random.randomValue(r, 2) }
    : value;
const randomOp = (path) =>
  r.chance(0.15) ? { path, remove: true } : { path, value: random.randomValue(r, 2) };
function randomCase(i) {
  const root = randomRoot(random.randomValue(r, 3));
  const rendered = random.render(r, root);
  const text = i % 4 === 3 ? random.mutate(r, rendered) : rendered;
  const op = randomOp(random.randomPath(r, r.pick(random.allPaths(root)), APPEND));
  push(runOps(text, [op], r.pick([cliOptions, tabOptions])));
  return text;
}
const randomDocs = Array.from({ length: 300 }, (_, i) => randomCase(i));

// ---------------------------------------------------------------- parse

const parseCase = (text, options) => {
  const errors = [];
  const value = jsonc.parse(text, errors, options);
  return {
    text,
    options,
    undefined: value === undefined,
    value: value === undefined ? null : JSON.stringify(value),
    errors: errors.map((e) => Object.assign({ code: e.error }, byteSpan(text, e.offset, e.length))),
  };
};
// Number formatting does not depend on the options: one option set each.
const numbers = random.randomNumbers(r, 400);
const numberTexts = [
  JSON.stringify(numbers),
  `[${numbers.map((n) => n.toExponential()).join(',')}]`,
];
const parseCases = [
  ...[...docs, ...corpus.parseOnlyTexts, ...randomDocs].flatMap((text) =>
    parseOptions.map((options) => parseCase(text, options)),
  ),
  ...numberTexts.map((text) => parseCase(text, parseOptions[0])),
];

// ---------------------------------------------------------------- format

function formatCase(text, options, range) {
  const rec = { text, options };
  if (range) rec.range = byteSpan(text, range.offset, range.length);
  const list = jsonc.format(text, range, options);
  if (list.some((e) => e.content === undefined)) rec.error = 'undefined content';
  else rec.edits = edits(text, list);
  return rec;
}
// The middle third of text, not splitting surrogate pairs.
const avoidSplit = (text, off) => off + (/[\udc00-\udfff]/.test(text.charAt(off)) ? 1 : 0);
const middleThird = (text) => {
  const third = Math.floor(text.length / 3);
  const offset = avoidSplit(text, third);
  return { offset, length: avoidSplit(text, Math.max(offset, 2 * third)) - offset };
};
const formatCasesFor = (text, optionsList) =>
  optionsList.flatMap((options) =>
    [undefined, middleThird(text)].map((range) => formatCase(text, options, range)),
  );
const formatCases = [
  ...docs.flatMap((text) => formatCasesFor(text, formatOptions)),
  ...formatCasesFor(corpus.deep(100), formatOptions.slice(0, 1)),
  ...formatCasesFor(corpus.deep(99), formatOptions.slice(0, 1)),
  ...randomDocs.slice(0, 40).flatMap((text) => formatCasesFor(text, formatOptions)),
];

// ---------------------------------------------------------------- write

// JSON Lines: a header record, then one case per line. Compact, readable in
// diffs, and outside prettier's file types.
const write = (name, header, cases) => {
  const lines = [{ generator: 'gen.mjs', jsoncParser: pkg.version, ...header }, ...cases];
  writeFileSync(join(here, name), lines.map((line) => JSON.stringify(line)).join('\n') + '\n');
  console.log(`${name}: ${cases.length} cases`);
};
write('parse.jsonl', {}, parseCases);
write('format.jsonl', {}, formatCases);
write('modify.jsonl', { docs: modifyDocs }, modifyCases);
