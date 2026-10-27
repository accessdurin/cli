import { expect, it } from 'vitest';
import { personalBootstrapSchema } from './personal-bootstrap.js';

it('accepts the current app bootstrap response with organization entitlements', () => {
  expect(personalBootstrapSchema.parse(currentBootstrap()).entitlements).toEqual({
    organizationId: 'org_one',
    package: 'standard',
  });
});

const currentBootstrap = () => ({
  organization: { id: 'org_one', name: 'Fixture Org', environment: 'sandbox' },
  membership: {
    principalId: 'user_one',
    role: 'admin',
    active: true,
    revocationEpoch: 0,
  },
  entitlements: { organizationId: 'org_one', package: 'standard' },
  session: {
    mode: 'workos',
    principalId: 'user_one',
    name: 'Fixture User',
    email: 'fixture@example.test',
    role: 'admin',
    organizationId: 'org_one',
    organizations: [{ id: 'org_one', name: 'Fixture Org' }],
  },
  mcp: { status: 'ready', url: `https://mcp.example.test/mcp/u/${'a'.repeat(64)}` },
  settings: { retention: 'metadata_only', environment: 'sandbox' },
});
