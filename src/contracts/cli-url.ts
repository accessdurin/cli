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
