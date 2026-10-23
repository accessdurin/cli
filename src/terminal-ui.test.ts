import * as p from '@clack/prompts';
import { expect, it, vi } from 'vitest';
import { terminalUi } from './terminal-ui.js';

vi.mock('@clack/prompts', () => ({
  isCancel: () => false,
  cancel: vi.fn<() => void>(),
  select: vi.fn<(options: { options: { value: string }[] }) => Promise<string>>(
    async ({ options }) => options[0]!.value,
  ),
  note: vi.fn<(message: string, title: string) => void>(),
  outro: vi.fn<(message: string) => void>(),
  log: { info: vi.fn<(message: string) => void>(), step: vi.fn<(message: string) => void>() },
}));
vi.mock('./browser.js', () => ({ openBrowser: async () => {} }));

const hostile = 'Acme\x1b]52;c;ZWNobyBwd25lZA==\x07\x1b[1A\x1b[2K\u009b2J\u202e Corp';
const rendered = (): string =>
  [
    ...vi
      .mocked(p.select)
      .mock.calls.flatMap(([{ options }]) => options.map((option) => String(option.label))),
    ...vi.mocked(p.note).mock.calls.map(([message]) => String(message)),
    ...vi.mocked(p.log.info).mock.calls.map(([message]) => String(message)),
  ].join('\n');

it('renders server-supplied names and codes without terminal controls', async () => {
  const ui = terminalUi();
  await ui.organizations.choose([{ id: 'org_one', name: hostile }]);
  await ui.account.authorize({ code: hostile, url: 'https://auth.example.test/device' }, 'login');
  await ui.onboarding.payment(hostile);
  ui.agent.ready({
    organization: hostile,
    agent: 'Codex',
    profileId: 'a'.repeat(64),
    toolCount: 1,
  });
  const text = rendered();
  expect(text.match(/Acme Corp/gu)).toHaveLength(4);
  expect(text.replaceAll('\n', '')).not.toMatch(/[\p{Cc}\u202e]/u);
});
