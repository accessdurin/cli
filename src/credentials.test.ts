import { beforeEach, describe, expect, it, vi } from 'vitest';
import { NativeCredentialStore } from './credentials.js';

const state = vi.hoisted(() => ({ values: new Map<string, string>(), locked: false }));
vi.mock('@napi-rs/keyring', () => ({
  AsyncEntry: class {
    private readonly key: string;
    constructor(service: string, key: string) {
      this.key = `${service}:${key}`;
    }
    async getPassword() {
      if (state.locked) throw new Error('native provider failure');
      return state.values.get(this.key);
    }
    async setPassword(value: string) {
      if (state.locked) throw new Error(value);
      state.values.set(this.key, value);
    }
    async deleteCredential() {
      if (state.locked) throw new Error('native provider failure');
      return state.values.delete(this.key);
    }
  },
}));
beforeEach(() => {
  state.values.clear();
  state.locked = false;
});

describe('OS credential store boundary', () => {
  it('stores and rotates independent profile grants and removes only the selected grant', async () => {
    const store = new NativeCredentialStore();
    expect(await store.read('profile-one')).toBeNull();
    await store.write('profile-one', 'first-synthetic-grant');
    await store.write('profile-two', 'second-synthetic-grant');
    await store.write('profile-one', 'rotated-synthetic-grant');
    expect(await store.read('profile-one')).toBe('rotated-synthetic-grant');
    await store.remove('profile-one');
    expect(await store.read('profile-one')).toBeNull();
    expect(await store.read('profile-two')).toBe('second-synthetic-grant');
    await store.remove('profile-one');
  });
  it.each(['read', 'write', 'remove'] as const)(
    'fails closed when the OS credential store cannot %s',
    async (operation) => {
      state.locked = true;
      const store = new NativeCredentialStore();
      const actions = {
        read: () => store.read('profile'),
        write: () => store.write('profile', 'synthetic-secret'),
        remove: () => store.remove('profile'),
      };
      await expect(actions[operation]()).rejects.toThrow('OS credential store unavailable');
      expect(state.values.size).toBe(0);
    },
  );
});
