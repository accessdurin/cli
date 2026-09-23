import { CliError } from './cli-options.js';
import type { CredentialStore } from './ports/credential-store.js';
import type { AsyncEntry } from '@napi-rs/keyring';

export class NativeCredentialStore implements CredentialStore {
  constructor(private readonly service = 'com.getdurin.cli') {}

  async read(key: string): Promise<string | null> {
    return (await this.use(key, (entry) => entry.getPassword())) ?? null;
  }

  async write(key: string, value: string): Promise<void> {
    await this.use(key, (entry) => entry.setPassword(value));
  }

  async remove(key: string): Promise<void> {
    await this.use(key, (entry) => entry.deleteCredential());
  }

  private async use<T>(key: string, operation: (entry: AsyncEntry) => Promise<T>): Promise<T> {
    try {
      const { AsyncEntry } = await import('@napi-rs/keyring');
      return await operation(
        new AsyncEntry(this.service, key, { linux: { store: 'secret-service' } }),
      );
    } catch {
      throw new CliError(
        'OS credential store unavailable. Unlock your credential store and run durin again.',
      );
    }
  }
}
