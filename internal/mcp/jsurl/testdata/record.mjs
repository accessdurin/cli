// Records how Node's WHATWG URL implementation parses a corpus of inputs, so the Go port can be
// checked against it without Node. Run: node record.mjs > urls.json
const cases = [
  ['https://app.example.test/mcp/u/abc'],
  ['https://APP.Example.TEST:443/a/./b/../c?x=1 2#frag ment'],
  ['http://example.com:80/'],
  ['http://example.com:0080/p'],
  ['https://example.com:65536/'],
  ['https://user:pa ss@example.com/'],
  ['https://example.com'],
  ['https://example.com?q'],
  ['  https://example.com/a\tb\nc  '],
  ['https://exa mple.com/'],
  ['https://example.com/a%2e%2E/b'],
  ['https://example.com/a/%2e%2e/b'],
  ['https://example.com/a^b{c}`d"e<f>g'],
  ["https://example.com/?a='b'&c=<d>"],
  ['https://example.com\\a\\b'],
  ['https://0x7f.1/'],
  ['https://127.0.0.1./'],
  ['https://1.2.3.256/'],
  ['https://[::1]:8080/'],
  ['https://[2001:db8:0:0:1:0:0:1]/'],
  ['https://[::ffff:1.2.3.4]/'],
  ['https://%41.com/'],
  ['https://ex%2fample.com/'],
  ['file:///C:/Windows/System32/calc.exe'],
  ['file://attacker.example.test/share/payload.exe'],
  ['smb://attacker.example.test/share'],
  ['ms-msdt:/id%20PCWDiagnostic'],
  ['javascript:alert(1)'],
  ['mailto:a b@c'],
  ['http:example.com/x'],
  ['https:/path'],
  [''],
  ['not a url'],
  ['/.well-known/oauth-protected-resource/mcp/u/abc', 'https://app.example.test/mcp/u/abc?x=1'],
  ['/.well-known/openid-configuration', 'https://auth.example.test/tenant/'],
  ['/', 'https://app.example.test/mcp/u/abc'],
  ['/token', 'https://auth.example.test/realms/x'],
  ['//other.example/x', 'https://app.example.test/a'],
  ['?q=1', 'https://app.example.test/a/b'],
  ['#h', 'https://app.example.test/a/b?q'],
  ['../x', 'https://app.example.test/a/b/c'],
  ['x', 'https://app.example.test/a/b/c'],
  ['https://other.example/', 'https://app.example.test/a'],
  ['HTTPS://Example.COM/A?B#C'],
  ['https://example.com/ü?ü#ü'],
  ['https://example.com:/x'],
  ['https://@example.com/'],
  ['https://a@b@example.com/'],
  ['http://[::1'],
  ['https://example.com/%zz'],
  ['wss://example.com:443/'],
  ['https://localhost:3000/cb'],
];
const out = cases.map(([input, base]) => {
  try {
    const u = base === undefined ? new URL(input) : new URL(input, base);
    return {
      input, base: base ?? null, ok: true, href: u.href, origin: u.origin, protocol: u.protocol,
      username: u.username, password: u.password, host: u.host, hostname: u.hostname, port: u.port,
      pathname: u.pathname, search: u.search, hash: u.hash, canParse: URL.canParse(input, base),
    };
  } catch {
    return { input, base: base ?? null, ok: false };
  }
});
const params = ['a=1&b=2', 'a=1&a=2&b=%20+x', 'x=%zz&=y&z', ''].map((q) => {
  const sp = new URLSearchParams(q);
  sp.set('a', 'v w/é~!*()\'');
  sp.append('c', '');
  return { query: q, pairs: [...new URLSearchParams(q)], afterSet: sp.toString() };
});
const auth = new URL('https://auth.example.test/authorize?existing=a b');
auth.searchParams.set('response_type', 'code');
auth.searchParams.set('redirect_uri', 'http://127.0.0.1:5555/oauth/callback');
auth.searchParams.set('scope', 'a b offline_access');
auth.searchParams.append('prompt', 'consent');
console.log(JSON.stringify({ cases: out, params, authorizationUrl: auth.href }, null, 2));
