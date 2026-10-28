/** Lowercase, without one trailing dot, and with IP addresses written as a URL writes them. */
const normalHost = (hostname: string): string => {
  const host = hostname.toLowerCase().replace(/\.$/u, '');
  const bare = host.replace(/^\[(.*)\]$/u, '$1');
  const candidate = `http://${bare.includes(':') ? `[${bare}]` : bare}`;
  return URL.canParse(candidate) ? new URL(candidate).hostname : host;
};
/**
 * localhost and its subdomains, 127.0.0.0/8, ::1, IPv4-mapped 127.x.x.x, and the unspecified
 * addresses 0.0.0.0 and ::, which reach this computer too.
 */
const LOOPBACK_HOSTS = [
  /^(?:.+\.)?localhost$/u,
  /^127\.\d+\.\d+\.\d+$/u,
  /^0\.0\.0\.0$/u,
  /^\[::1?\]$/u,
  /^\[::ffff:(?:7f[\da-f]{2}:[\da-f]{1,4}|0:0)\]$/u,
];
export const loopbackCliHost = (hostname: string): boolean => {
  const host = normalHost(hostname);
  return LOOPBACK_HOSTS.some((pattern) => pattern.test(host));
};

export function safeCliUrl(value: string, development: boolean): boolean {
  if (!URL.canParse(value)) return false;
  const url = new URL(value);
  const local = [development, url.protocol === 'http:', loopbackCliHost(url.hostname)].every(
    Boolean,
  );
  return [
    url.protocol === 'https:' || local,
    !url.username,
    !url.password,
    !url.hash,
    !url.search,
    !/[\r\n]/u.test(value),
  ].every(Boolean);
}
/**
 * A self-hosted issuer is stored as written. Spaces, NUL, and backslashes are refused, and so is
 * any spelling whose URL form is not the string itself (`HTTPS://`, an explicit `:443`, or `//`).
 */
const OPAQUE_ISSUER = new RegExp(`[\\s${String.fromCharCode(0)}\\\\]`, 'u');
const opaqueIssuer = (value: string): boolean => OPAQUE_ISSUER.test(value) || !URL.canParse(value);
const extraSlash = (value: string): boolean => {
  const scheme = value.indexOf('://');
  return scheme < 0 || value.slice(scheme + 3).includes('//');
};
/** `https://<host>` and `https://<host>/` are the same issuer. The URL form adds the slash. */
const hrefMatches = (value: string, url: URL): boolean =>
  url.href === value || (url.pathname === '/' && url.href === `${value}/`);
export function roundTripCliUrl(value: string): boolean {
  if (opaqueIssuer(value)) return false;
  const url = new URL(value);
  return !extraSlash(value) && hrefMatches(value, url);
}
