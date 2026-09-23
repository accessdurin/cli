import { randomUUID } from 'node:crypto';
import { expect, it } from 'vitest';
import { NativeCredentialStore } from './credentials.js';

it.skipIf(process.env.DURIN_TEST_KEYRING !== '1')(
  'round-trips and removes isolated synthetic grants in the actual OS credential store',
  async () => {
    const store = new NativeCredentialStore(`com.getdurin.cli.test.${randomUUID()}`);
    try {
      expect(await store.read('one')).toBeNull();
      await store.write('one', 'synthetic-grant-one');
      await store.write('two', 'synthetic-grant-two');
      await store.write('one', 'synthetic-grant-rotated');
      expect(await store.read('one')).toBe('synthetic-grant-rotated');
      await store.remove('one');
      expect(await store.read('one')).toBeNull();
      expect(await store.read('two')).toBe('synthetic-grant-two');
      await store.remove('one');
    } finally {
      await store.remove('one');
      await store.remove('two');
    }
  },
);
