import { z } from 'zod';

export const endpointSchema = z
  .string()
  .max(2048)
  .refine((value) => {
    if (!URL.canParse(value)) return false;
    const url = new URL(value);
    return [
      url.protocol === 'https:',
      !url.username,
      !url.password,
      !url.search,
      !url.hash,
      /^\/mcp\/u\/[a-f0-9]{64}$/.test(url.pathname),
      !/[\r\n\t]/.test(value),
      value === url.href,
    ].every(Boolean);
  }, 'Use the HTTPS personal /mcp/u/... endpoint copied from Durin, without credentials or query parameters.');

export const validateEndpoint = (value: string | undefined): string | undefined => {
  if (!value) return 'Endpoint URL is required.';
  return endpointSchema.safeParse(value).success
    ? undefined
    : 'Use the HTTPS personal /mcp/u/... endpoint copied from Durin, without credentials or query parameters.';
};
