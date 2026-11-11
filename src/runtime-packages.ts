import { createRequire } from 'node:module';
import { cp, readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { z } from 'zod';

const manifestSchema = z.object({
  dependencies: z.record(z.string(), z.string()).default({}),
  optionalDependencies: z.record(z.string(), z.string()).default({}),
});
export const packageDirectory = (name: string, from: string): string =>
  dirname(createRequire(from).resolve(`${name}/package.json`));
const optionalDirectory = (name: string, from: string): string | null => {
  try {
    return packageDirectory(name, from);
  } catch (error) {
    if (z.object({ code: z.literal('MODULE_NOT_FOUND') }).safeParse(error).success) return null;
    throw error;
  }
};
export async function copyRuntimePackage(directory: string, target: string): Promise<void> {
  await cp(directory, target, {
    recursive: true,
    dereference: true,
  });
  const manifest = manifestSchema.parse(
    JSON.parse(await readFile(join(directory, 'package.json'), 'utf8')),
  );
  const from = join(directory, 'package.json');
  await Promise.all(
    Object.keys(manifest.dependencies).map((name) =>
      copyRuntimePackage(packageDirectory(name, from), join(target, 'node_modules', name)),
    ),
  );
  await Promise.all(
    Object.keys(manifest.optionalDependencies).map(async (name) => {
      const path = optionalDirectory(name, from);
      if (path) await copyRuntimePackage(path, join(target, 'node_modules', name));
    }),
  );
}
