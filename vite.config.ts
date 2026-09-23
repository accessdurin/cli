import { defineConfig } from 'vite';
import { resolve } from 'node:path';
import { builtinModules } from 'node:module';

export default defineConfig({
  resolve: {
    conditions: ['node', 'module', 'development|production'],
    mainFields: ['module', 'main'],
  },
  build: {
    target: 'node24',
    lib: {
      entry: resolve(import.meta.dirname, 'src/cli.ts'),
      formats: ['es'],
      fileName: 'cli',
    },
    rolldownOptions: {
      platform: 'node',
      external: [...builtinModules, /^node:/, '@napi-rs/keyring'],
    },
    outDir: 'dist',
    emptyOutDir: true,
  },
});
