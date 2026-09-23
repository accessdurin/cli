export const loopbackCliHost = (hostname: string): boolean =>
  ['localhost', '127.0.0.1', '[::1]'].includes(hostname);

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
