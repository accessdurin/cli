// Regenerates the differential fixtures for package weburl from Node's own URL,
// URLSearchParams and zod's z.url(). Run with Node 22 from anywhere:
//
//   node internal/weburl/testdata/gen.mjs [wpt-resources-dir]
//
// Without a directory, the Web Platform Tests URL resources are fetched from GitHub at the
// pinned commit below. Every expected value in the fixtures comes from this Node process, never
// from WPT's own expectations, so the Go tests check "what Node does", including where Node
// differs from the URL Standard. `go test` reads only the JSON files this script writes.

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { z } from '/home/user/cli/node_modules/zod/index.js';

const WPT_COMMIT = 'e420bf1dbd2700e63d40ba5b425fa6cebad3f316';
const here = dirname(fileURLToPath(import.meta.url));
const zodPackage = JSON.parse(
  readFileSync('/home/user/cli/node_modules/zod/package.json', 'utf8'),
);

async function wpt(name) {
  const dir = process.argv[2];
  if (dir) return JSON.parse(readFileSync(join(dir, name), 'utf8'));
  const url = `https://raw.githubusercontent.com/web-platform-tests/wpt/${WPT_COMMIT}/url/resources/${name}`;
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${url}: ${response.status}`);
  return response.json();
}

// JSON can carry lone surrogates that Go cannot represent. URL and URLSearchParams take
// USVStrings, which replace them with U+FFFD, so the well-formed string behaves identically.
const wellFormed = (s) => (typeof s === 'string' ? s.toWellFormed() : s);

const FIELDS = [
  'href',
  'origin',
  'protocol',
  'username',
  'password',
  'host',
  'hostname',
  'port',
  'pathname',
  'search',
  'hash',
];
// Empty getters are left out to keep the fixtures small; the Go tests read a missing one as ''.
const snapshot = (url) =>
  Object.fromEntries(FIELDS.filter((f) => url[f] !== '').map((f) => [f, url[f]]));

// Node 22's URL.canParse is wrong once V8 optimizes it (after a few thousand calls): its fast
// API path then reads one-byte strings holding U+0080..U+00FF (such as "http://ß/") as if they
// were UTF-8 and rejects them, while new URL() accepts them. The oracle is therefore always the
// constructor; package weburl's CanParse follows the constructor too.
const parses = (input, base = null) => {
  try {
    return base === null ? new URL(input) : new URL(input, base);
  } catch {
    return null;
  }
};

const parseCases = [];
const seenParse = new Set();
function addParse(input, base = null, source = 'cli') {
  input = wellFormed(input);
  base = wellFormed(base);
  const key = JSON.stringify([input, base]);
  if (seenParse.has(key)) return;
  seenParse.add(key);
  const c = { source, input };
  if (base !== null) c.base = base;
  const url = parses(input, base);
  if (!url) {
    c.failure = true;
  } else {
    Object.assign(c, snapshot(url));
    if (url.search) c.searchParams = [...url.searchParams];
  }
  parseCases.push(c);
}

// ---- WPT inputs -------------------------------------------------------------------------
for (const t of [
  ...(await wpt('urltestdata.json')),
  ...(await wpt('urltestdata-javascript-only.json')),
]) {
  if (typeof t === 'string') continue;
  addParse(t.input, t.base ?? null, 'wpt');
}

for (const t of [...(await wpt('IdnaTestV2.json')), ...(await wpt('toascii.json'))]) {
  if (typeof t === 'string') continue;
  addParse(`https://${t.input}/x`, null, 'idna');
}

// ---- CLI-specific corpus ----------------------------------------------------------------
const HEX64 = 'a'.repeat(32) + '0123456789abcdef'.repeat(2);
const HOSTS = [
  // loopback names and addresses in every spelling a parser normalizes
  'localhost', 'LOCALHOST', 'LocalHost', 'localhost.', 'localhost..', 'sub.localhost',
  'a.b.localhost', 'SUB.LOCALHOST.', 'localhost.example', 'notlocalhost', '.localhost',
  '127.0.0.1', '127.1', '127.0.1', '127.255.255.255', '127.0.0.1.', '127.0.0.1..',
  '0177.0.0.1', '0177.1', '0x7f.0.0.1', '0x7f.1', '0X7F.1', '0x7F000001', '0x7f000001',
  '2130706433', '017700000001', '0x7f.0x0.0x0.0x1', '127.00.00.01', '127.0.0.01',
  '0.0.0.0', '0', '0x0', '00', '0.0', '0x', '0x.0x.0', '0..0', '1.2.3.4.5', '256.0.0.1',
  '4294967295', '4294967296', '0x100000000', '0xffffffff', '1.2.3.09', '09.1.1.1',
  '1.2.3.0x', '1.2.3.4.', '1.2.3.4..', 'a.1', '1.a', 'a.0x1', 'foo.09', 'foo.0x', 'foo.1.',
  '999999999999999999999', '0x7f.0.0.1.', '127.0.0.1x', '127.0.0.0x1',
  '[::1]', '[::]', '[0:0:0:0:0:0:0:1]', '[0:0:0:0:0:0:0:0]', '[::0]', '[0::1]', '[::0:1]',
  '[::ffff:127.0.0.1]', '[::ffff:7f00:1]', '[::FFFF:7F00:0001]', '[::ffff:7f00:0]',
  '[::ffff:0:0]', '[::ffff:0.0.0.0]', '[::127.0.0.1]', '[::ffff:7f01:203]',
  '[0:0:0:0:0:ffff:7f00:1]', '[::ffff:127.1]', '[::ffff:7f00:1%25eth0]', '[fe80::1%25eth0]',
  '[1:2:3:4:5:6:7:8]', '[1:2:3:4:5:6:7:8:9]', '[1:0:0:2::3:0]', '[1::2:0:0:0:3]',
  '[2001:db8::1]', '[2001:DB8::1]', '[::1', '::1', '[::1]x', '[::1].', '[]', '[:]',
  '[::1:]', '[:::1]', '[1::2::3]', '[::1.2.3]', '[::1.2.3.4.5]', '[::01.2.3.4]',
  '[::1.2.3.256]', '[0x7f::1]', '[g::1]', '[::ffff:1.2.3.4]', '[1:2:3:4:5:6:1.2.3.4]',
  '[1:2:3:4:5:6:7:1.2.3.4]', '[ ::1]',
  // durin deployment hosts and look-alikes
  'app.getdurin.com', 'App.GetDurin.com', 'APP.GETDURIN.COM', 'app.getdurin.com.',
  'app.getdurin.com..', 'mcp.getdurin.com', 'auth.getdurin.com', 'moria.app.getdurin.com',
  'moria.mcp.getdurin.com', 'resilient-tiger-64-staging.authkit.app', 'tenant.authkit.app',
  'Tenant.AuthKit.App', 'a.b.authkit.app', 'xn--tenant.authkit.app', '-tenant.authkit.app',
  'tenant-.authkit.app', 't--x.authkit.app', 'tenant.authkit.app.', 'authkit.app',
  'x'.repeat(63) + '.authkit.app', 'x'.repeat(64) + '.authkit.app', 'api.workos.com',
  'API.WORKOS.COM', 'api.workos.com.', 'example.com', 'EXAMPLE.com', 'example.com.',
  'example.com..', 'a..b', '.example.com', '..example.com', 'a.b.c.d.e.f',
  // non-ASCII and IDNA
  'münchen.de', 'MÜNCHEN.de', 'xn--mnchen-3ya.de', 'XN--MNCHEN-3YA.DE', 'faß.de', 'FASS.de',
  'ＡＰＰ．ｇｅｔｄｕｒｉｎ．ｃｏｍ', 'app。getdurin。com', 'app｡getdurin｡com', 'app．getdurin．com',
  'аpp.getdurin.com', '☃.net', 'xn--n3h.net', 'example.com\u00AD', 'exam\u00ADple.com',
  'ex\u200Dample.com', 'ex\u200Cample.com', 'a\u200D.com', 'xn--', 'xn--a', 'xn--a.com',
  'xn--ls8h.la', '💩.la', '日本.jp', 'ｅｘａｍｐｌｅ.com', 'éxample.com', 'e\u0301xample.com',
  'ℂ.com', 'ⅷ.com', 'ß', 'ς.gr', 'σ.gr', 'ı.com', 'İ.com', 'ǅ.com', '\u0661\u0662.com',
  'a\u0661.com', '\u05D0.com', '\u05D0a.com', 'xn--4db.com', '\u0627\u0644.com',
  '\uFFFD.com', '\u2028.com', 'a\u3002', '\u0000.com', 'a\uFE0F.com', '\u00A0.com', 'x\u180Ey',
  'xn--zca.com', 'xn--55qx5d.cn', 'xn--1ch.com', 'xn--ab-.com', 'xn--a-ecp.ru',
  // forbidden and odd code points
  'exa mple.com', 'exa%20mple.com', 'example%2Ecom', '%41pp.getdurin.com', '%2e', '%zz.com',
  '%e2%98%83.net', '%ef%bc%85.com', 'ex<ample.com', 'host^', 'host|x', 'a%7Cb', 'a"b',
  "a'b", 'a`b', 'a{b', 'a}b', 'a!b', 'a$b', 'a&b', 'a(b)', 'a*b', 'a+b', 'a,b', 'a;b',
  'a=b', 'a_b', 'a~b', 'a%00b', 'a\u007Fb', 'a\u0001b', 'a\u001Fb', 'a\u0080b', '*', '_',
  // hostile punycode
  'xn--99999999999999999999', 'xn--a-99999999999', 'xn--ab-cd-ef', `xn--a-${'z'.repeat(60)}`,
  'xn--1234567890', 'xn--zzzzzzzzzzzzz', 'xn--A-ecp.ru', 'xn--é.com', 'XN--ü.com', 'xn--a-é',
  'xn--9', 'xn--0', 'xn--a-9', 'xn--ls8h-', 'xn---ls8h',
  // percent-encoded bytes that are not UTF-8
  '%ed%a0%80.com', '%c0%ae.com', '%f4%90%80%80.com', '%ff.com', '%c3.com', '%c3%a9.com',
  '%E2%80%8D.com', '%e3%80%82com', 'a%2e%2E', '%5b::1%5d', '%3A',
];
const PORTS = ['', ':', ':80', ':443', ':8080', ':3000', ':0', ':00080', ':65535', ':65536',
  ':0443', ':+1', ':a', ':-1', ':1e3', ':99999999999'];
const PATHS = ['', '/', `/mcp/u/${HEX64}`, '/oauth/callback?code=abc&state=xyz', '/a/../b',
  '/%2e%2E/x', '/./x', '/a b', '?q=1', '#frag', '/x?', '/x#', '/x?#', '\\x\\y'];
const SCHEMES = ['http://', 'https://'];

for (const host of HOSTS) {
  for (const scheme of SCHEMES)
    for (const port of PORTS) addParse(`${scheme}${host}${port}/`);
  for (const scheme of SCHEMES)
    for (const path of PATHS) addParse(`${scheme}${host}${path}`);
  for (const scheme of ['HTTPS://', 'ws://', 'wss://', 'ftp://', 'file://', 'mcp://', 'foo://'])
    addParse(`${scheme}${host}/`);
  addParse(`https://${host}:8443/mcp/u/${HEX64}`);
  addParse(`https://user:pass@${host}/`);
  // cli-host.ts builds `http://${host}` from host[:port] strings
  addParse(`http://${host}`);
}
for (const port of PORTS)
  for (const scheme of ['ws://', 'wss://', 'ftp://', 'foo://', 'file://', 'mcp://'])
    addParse(`${scheme}example.com${port}/x`);

const WHOLE = [
  'https://app.getdurin.com', 'https://app.getdurin.com/', 'https://app.getdurin.com//',
  'https://app.getdurin.com/?', 'https://app.getdurin.com/#', 'https://app.getdurin.com?',
  'https://app.getdurin.com#', 'https://app.getdurin.com:443', 'https://app.getdurin.com:443/',
  'http://app.getdurin.com:80', 'https://app.getdurin.com:80', 'http://localhost:8787',
  'http://127.0.0.1:8787/', 'http://[::1]:8787', 'http://localhost:8787/x',
  ' https://app.getdurin.com', 'https://app.getdurin.com ', '\thttps://app.getdurin.com',
  'https://app.getdurin.com\n', 'https://app.getdurin.com\r\n', '\u00A0https://a.com',
  'https://a.com\u00A0', '\u000Bhttps://a.com', '\u000Chttps://a.com', '\u0000https://a.com',
  'https://a.com\u0000', '\u001Fhttps://a.com\u001F', '\uFEFFhttps://a.com',
  'https://a\r\n.com/', 'ht\ttps://a.com', 'https://a.c\tom/', `https://a.com/mcp/u/\n${HEX64}`,
  `https://a.com/mcp/u/${HEX64}\n`, `https://a.com/mcp/u/${HEX64}\t`, 'https://a.com/x\ry',
  'https://a.com/?a\nb', 'https://a.com/#a\tb', 'https://us\ner@a.com/', 'https://a.com:4\n43/',
  'https:\\\\a.com\\x', 'https:\\/a.com', 'https:/\\a.com', 'https:a.com', 'https:/a.com',
  'https:///a.com', 'https:////a.com', 'http:a.com', 'https:', 'https:/', 'https://',
  'https:///', 'https://?x', 'https://#x', 'https://:443', 'https://user@', 'https://@a.com',
  'https://user:pass@a.com', 'https://user@a.com', 'https://:pass@a.com', 'https://:@a.com',
  'https://u:p:q@a.com', 'https://a@b@c.com', 'https://a.com@evil.com',
  'https://evil.com\\@a.com', 'https://evil.com#@a.com', 'https://evil.com?@a.com',
  'https://a.com%40evil.com', 'https://u%40x:p%3Ay@a.com', 'https://ü:ñ@a.com',
  'https://u p:q r@a.com', 'https://u\\p@a.com', 'https://a.com/%', 'https://a.com/%zz',
  'https://a.com/é', 'https://a.com/?é', 'https://a.com/#é', "https://a.com/'\"<>`{}|^",
  "https://a.com/?'\"<>`{}|^", "https://a.com/#'\"<>`{}|^", 'https://a.com/? #',
  'http://a.com/?a=1&a=2&b=%20&c=+&d', 'https://a.com/%2F%5C', 'https://a.com/./..',
  'https://a.com/a/./b/../c', 'https://a.com/.%2e/x', 'https://a.com/a/%2e%2e', 'https://a.com/..',
  'https://a.com/a/..?x', 'file:///etc/passwd', 'file://localhost/x', 'file://LOCALHOST/x',
  'file://127.0.0.1/x', 'file:c:\\x', 'file:///C:/x/../..', 'file://host/C|/x', 'file:',
  'mcp://server/x', 'mcp:x', 'mailto:a@b.com', 'javascript:alert(1)', 'data:text/plain,hi',
  'blob:https://a.com/uuid', 'blob:foo:bar', 'blob:http://[::1]:80/x', 'urn:uuid:1',
  'sc://ñ/', 'sc://a b/', 'sc://%/', 'sc://[::1]/', 'sc://x:65536', 'sc: x ', 'sc:x ?q',
  'sc:x #f', 'foo://a.com', 'foo://A.COM/', 'foo://a.com:80/', 'foo:///x', 'foo:/x',
  'foo:x/y', 'foo:/.//x', 'foo://', 'foo:', 'ws://a.com:80', 'wss://a.com:443',
  'ftp://a.com:21', 'http://a.com:443', 'https://a.com:80', 'HTTP://A.COM/', 'Https://a.com',
  'h t t p://a.com', '1http://a.com', 'h+t-t.p://a.com', 'h_ttp://a.com', '://a.com', '//a.com',
  '/a', 'a.com', '', ' ', 'http', 'http:', '?', '#', 'localhost', 'localhost:3000',
  '127.0.0.1:3000', 'app.getdurin.com', 'https://a.com:1:2', 'https://a.com:/x',
  'https://[::1]:', 'http://[::1]:80', 'https://[::1]:443/', 'http://0x7f.1:80/',
  'https://localhost./', 'http://localhost.:8787/', 'https://%6c%6f%63%61%6c%68%6f%73%74/',
  'https://local%68ost/', 'https://%31%32%37.0.0.1/', 'https://0x%37f.1/', 'http://1.2.3.4%2e/',
  'https://a.com/' + 'x'.repeat(3000), 'https://' + 'a.'.repeat(130) + 'com/',
];
for (const input of WHOLE) addParse(input);

// relative resolution as oauth-callback-input.ts, account-api.ts, setup.ts and device-auth do
const BASES = [
  'http://127.0.0.1:53682/oauth/callback', 'http://127.0.0.1/oauth/callback',
  'https://app.getdurin.com', 'https://app.getdurin.com/', 'https://app.getdurin.com/a/b?c#d',
  'http://localhost:8787', 'mcp://x/y', 'foo:opaque', 'file:///C:/a/b', 'about:blank',
  'https://api.workos.com/user_management/authorize', 'foo:a/b/c', 'foo:/a/b', 'foo:a/',
];
const TARGETS = [
  '/', '', '/oauth/callback', '/oauth/callback?code=abc&state=xyz',
  '/oauth/callback?code=a&code=b&state=s', '/oauth/callback?iss=https%3A%2F%2Fauth.getdurin.com',
  '/oauth/callback?code=%zz&state=a+b', '/oauth/callback?error=access_denied&state=',
  '/oauth/callback?__proto__=x&constructor=y', '/oauth/callback#frag', '/OAUTH/callback',
  '/oauth/callback/', '/oauth/./callback', '/oauth/x/../callback', '/oauth/%63allback',
  '//evil.com/oauth/callback', '///evil.com', '\\\\evil.com\\x', '/\\evil.com', 'http://evil.com/',
  'https://127.0.0.1:53682/oauth/callback', 'http:evil.com', 'http:/x', 'x', './x', '../x',
  '?code=1', '#x', '*', 'http://127.0.0.1:53682/oauth/callback', ' /oauth/callback ',
  '/oauth/callback\n?code=x', '/api/account', '/api/organizations/x?y=1', '/auth/login',
  '/auth/cli/token', '/user_management/authenticate', 'C:/x', '//', '/..', '..', '.',
  'x#f', '../y#f', '..#f', './#f', 'x/../..#f', '?q#f', '#', '#f',
];
for (const base of BASES) for (const target of TARGETS) addParse(target, base);
addParse('/api' + '/account', 'not a base');

// Random inputs built from the pieces parsers branch on, with a fixed seed so the fixtures
// are reproducible. They exercise state transitions no hand-written case thought of.
let seed = 0x5eed;
const random = () => {
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const pick = (list) => list[Math.floor(random() * list.length)];
const PIECES = ['http:', 'https:', 'file:', 'mcp:', 'sc:', 'blob:', 'HTTP:', '//', '/', '\\', '\\\\',
  '///', ':', '::', '@', ':@', 'u:p@', '[', ']', '[::1]', '[::', '%', '%2e', '%2E', '%41', '%zz',
  '%00', '%25', '.', '..', '...', '?', '#', '&', '=', '+', ' ', '\t', '\n', '\r', '\u0000',
  '\u001f', '\u007f', 'a', 'Z', 'x', '0', '1', '09', '0x', '0x7f', '127', '255', '256',
  '4294967295', '65535', '65536', '80', '443', 'localhost', 'xn--', 'xn--zca', 'C:', 'c|',
  'é', 'ß', 'ＡＢ', '。', '\u00ad', '\u200d', '☃', '\ufffd', '\u0627', '-', '_', '~', '^', '|',
  '`', '{', '}', '<', '>', '"', "'", '!', '$', '*', ',', ';'];
const randomInput = () => {
  let out = '';
  const n = 1 + Math.floor(random() * 10);
  for (let i = 0; i < n; i++) out += pick(PIECES);
  return out;
};
const RANDOM_BASES = [null, null, null, 'http://h/a/b?q#f', 'file:///C:/d/e', 'sc://h/p',
  'sc:opaque', 'https://u:p@h:8/x/'];
for (let i = 0; i < 6000; i++) {
  const base = pick(RANDOM_BASES);
  const scheme = base === null ? pick(['http:', 'https:', 'file:', 'sc:', 'ws:', 'mcp:']) : '';
  addParse(scheme + randomInput(), base, 'random');
}
for (let i = 0; i < 2000; i++) addParse(`${pick(['http://', 'https://', 'sc://', 'file://'])}${randomInput()}`, null, 'random');

// ---- setters ----------------------------------------------------------------------------
const setterCases = [];
function addSetter(setter, href, value, source = 'cli') {
  href = wellFormed(href);
  value = wellFormed(value);
  const url = parses(href);
  if (!url) return;
  url[setter] = value;
  setterCases.push({ source, setter, href, value, expected: snapshot(url) });
}
const setters = await wpt('setters_tests.json');
// Only the setters package weburl exposes (the CLI uses hostname and search).
for (const setter of ['hostname', 'host', 'search', 'hash']) {
  for (const t of setters[setter]) addSetter(setter, t.href, t.new_value, 'wpt');
}
const SETTER_BASES = [
  'https://app.getdurin.com/', 'https://app.getdurin.com.:8443/x?y#z', 'http://localhost../',
  'http://localhost./', 'http://[::1]:3000/', 'http://127.0.0.1./', 'https://user:pw@a.com/',
  'mcp://x/', 'mcp://x:1/p', 'foo:bar', 'foo:bar ', 'foo:bar #f', 'file:///x',
  'file://host/x', 'sc:/p', 'blob:https://a.com/x',
];
const HOSTNAME_VALUES = [...HOSTS, 'a:1', 'a/b', 'a?b', 'a#b', '', 'a\\b', ' a', 'a ',
  'h:a1', 'h:+1', 'h:-1', 'h: 1', 'h:81x', 'h:080', 'h:65536', 'h:', '[::1]:8', '[::1:8',
  'a\tb', 'a\nb', '[::1]:80', 'a@b', '%', 'xn--zz', '.', '..', 'a.:1'];
for (const base of SETTER_BASES) {
  for (const value of HOSTNAME_VALUES) addSetter('hostname', base, value);
  for (const value of HOSTNAME_VALUES.slice(HOSTS.length)) addSetter('host', base, value);
  for (const host of ['localhost', '127.0.0.1', '[::1]', 'app.getdurin.com', 'ß', 'a b'])
    for (const port of PORTS) addSetter('host', base, host + port);
  // canonicalOrigin: url.hostname = url.hostname.replace(/\.$/, '')
  for (const host of HOSTS) {
    const href = `https://${host}${base.includes(':8443') ? ':8443' : ''}/`;
    if (!parses(href)) continue;
    addSetter('hostname', href, new URL(href).hostname.replace(/\.$/u, ''));
  }
}
for (let i = 0; i < 1500; i++) {
  const base = pick(SETTER_BASES);
  addSetter(pick(['hostname', 'host', 'search', 'hash']), base, randomInput(), 'random');
}
const SEARCH_VALUES = ['', 'a=b', '?a=b', '??', '?', 'a b', '#', 'a=%zz', 'é', "'", '"<>`',
  'a=1&a=2', 'provider=authkit&organizationId=org_01', '\u0000', '\n', 'a\tb', ' x '];
const HASH_VALUES = ['', '#', 'x', '#x', '##', 'a b', 'é', '"<>`', '\u0000', 'a\nb'];
for (const base of SETTER_BASES) {
  for (const value of SEARCH_VALUES) addSetter('search', base, value);
  for (const value of HASH_VALUES) addSetter('hash', base, value);
}

// ---- URLSearchParams --------------------------------------------------------------------
const QUERIES = ['', '?', '??', '?a=b', 'a=b&c=d', 'a', '=b', '=', '&&', '&a&', 'a=b=c', '+',
  'a+b=c+d', '%20', '%zz', '%', '%e', '%e9', '%C3%A9', '%c3%a9%', '%2B', '%26=%3D',
  'a=1&a=2&a=3', 'a=1&b=2&a=3', '__proto__=x', 'constructor=y&toString=z', '1=a&b=2&0=c',
  'code=abc&state=xyz&iss=https%3A%2F%2Fauth.getdurin.com', 'code=&state=', 'é=ñ', '\u0000=\n',
  'a;b=c', 'a%00b=c', '%ef%bf%bd', '%ed%a0%80', '%f0%9f%92%a9', '%ff%fe', '%c3', 'a=%c3x',
  ' a = b ', 'a\u2028b', '%F0%9F%92', 'x=%E2%82'];
const searchParse = [];
for (const q of QUERIES) {
  const p = new URLSearchParams(wellFormed(q));
  const names = [...new Set([...p.keys(), 'missing', ''])];
  searchParse.push({
    input: wellFormed(q),
    size: p.size,
    lookups: names.map((n) => ({ name: n, get: p.get(n), getAll: p.getAll(n), has: p.has(n) })),
    entries: [...p],
    object: Object.entries(Object.fromEntries(p)).sort(([a], [b]) => (a < b ? -1 : 1)),
    string: p.toString(),
  });
}
// set/delete/append sequences, compared by their serialization
const searchOps = [];
const OPS = [
  [['set', 'a', 'x']], [['set', 'missing', 'x']], [['delete', 'a']], [['delete', 'missing']],
  [['append', 'a', 'y'], ['set', 'a', 'z']], [['set', 'a', '1'], ['append', 'a', '2'], ['delete', 'b']],
  [['append', 'é', 'ü ö'], ['set', '', '']],
];
for (const q of ['', 'a=1', 'a=1&b=2&a=3', 'b=1&a=2&a=3&c=4', '?a=1&a=1']) {
  for (const ops of OPS) {
    const p = new URLSearchParams(q);
    for (const [op, ...args] of ops) p[op](...args);
    searchOps.push({ input: q, ops, entries: [...p], string: p.toString() });
  }
}
const searchSerialize = [];
const chars = [];
for (let i = 0; i < 0x80; i++) chars.push(String.fromCharCode(i));
chars.push('é', 'ñ', '€', '💩', '\u00A0', '\uFEFF', '\uFFFD', '\u2028');
searchSerialize.push({ pairs: chars.map((c) => [c, c]) });
searchSerialize.push({ pairs: [['client_id', 'client_01H'], ['device_code', 'a b+c/d=e&f']] });
searchSerialize.push({ pairs: [['provider', 'authkit'], ['organizationId', 'org_01ABC']] });
searchSerialize.push({ pairs: [['grant_type', 'urn:ietf:params:oauth:grant-type:device_code']] });
searchSerialize.push({ pairs: [['', ''], ['a', ''], ['', 'b'], ['a b', 'c d']] });
searchSerialize.push({ pairs: [['k', chars.join('')]] });
for (const s of searchSerialize) s.string = new URLSearchParams(s.pairs).toString();

// ---- zod z.url() ------------------------------------------------------------------------
// z.url() is new URL(value.trim()) underneath, so the URL parse itself is covered by parse.json.
// These cases pin the trim (JavaScript's String.prototype.trim white space set) and the output.
const zodInputs = new Set([
  ...WHOLE, ...HOSTS.flatMap((h) => [`https://${h}/`, ` http://${h}:8080 `]),
  ' https://a.com ', '\u00A0https://a.com\u00A0', '\u2028https://a.com\u2029',
  '\u3000https://a.com', '\uFEFFhttps://a.com', '\u180Ehttps://a.com', '\u200Bhttps://a.com',
  '\u0085https://a.com', '\u1680https://a.com', '\u2000https://a.com\u200A',
  '\u202Fhttps://a.com\u205F', '\u000B\u000Chttps://a.com', '\u001Chttps://a.com',
  'https://a.com\u0000', '\u0000https://a.com',
]);
const zodUrl = z.url();
const zodCases = [...zodInputs].map((input) => {
  const r = zodUrl.safeParse(input);
  return r.success ? { input, ok: true, value: r.data } : { input, ok: false };
});

// ---- write ------------------------------------------------------------------------------
const meta = {
  generator: 'internal/weburl/testdata/gen.mjs',
  node: process.version,
  ada: process.versions.ada,
  icu: process.versions.icu,
  unicode: process.versions.unicode,
  zod: zodPackage.version,
  wptCommit: WPT_COMMIT,
};
// One case per line, so that a regenerated fixture diffs case by case.
const write = (name, data) => {
  const parts = Object.entries(data).map(
    ([key, list]) => `${JSON.stringify(key)}:[\n${list.map((c) => JSON.stringify(c)).join(',\n')}\n]`,
  );
  writeFileSync(join(here, name), `{"meta":${JSON.stringify(meta)},\n${parts.join(',\n')}}\n`);
};
write('parse.json', { cases: parseCases });
write('setters.json', { cases: setterCases });
write('searchparams.json', { parse: searchParse, ops: searchOps, serialize: searchSerialize });
write('zod.json', { cases: zodCases });
console.log(
  `parse ${parseCases.length}, setters ${setterCases.length}, searchparams ${searchParse.length}+${searchOps.length}+${searchSerialize.length}, zod ${zodCases.length}`,
  meta,
);
