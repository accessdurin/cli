/** JSON inputs only; recursively sorts object keys while preserving array order. */
export const canonicalJson = (value: unknown): string => JSON.stringify(normalize(value));
const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null;
function normalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(normalize);
  if (!record(value)) return value;
  return Object.fromEntries(
    Object.keys(value)
      .toSorted()
      .map((key) => [key, normalize(value[key])]),
  );
}
