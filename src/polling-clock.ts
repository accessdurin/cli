import { setTimeout } from 'node:timers/promises';
import type { PollingClock } from './ports/cli-auth.js';

export const pollingClock = (): PollingClock => ({
  now: () => Date.now(),
  sleep: async (ms) => {
    await setTimeout(ms);
  },
});
