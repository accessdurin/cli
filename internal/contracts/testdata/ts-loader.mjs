// Resolves the TypeScript sources' ".js" import specifiers to their ".ts" files, so gen.mjs
// can import src/ directly under node --experimental-strip-types.
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export async function resolve(specifier, context, next) {
  if (specifier.endsWith('.js') && context.parentURL?.includes('/src/')) {
    const candidate = new URL(specifier.replace(/\.js$/u, '.ts'), context.parentURL);
    if (existsSync(fileURLToPath(candidate))) return next(candidate.href, context);
  }
  return next(specifier, context);
}
