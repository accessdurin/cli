import { expect, it } from 'vitest';
import { selectOrganization } from './select-organization.js';

it('selects only verified memberships and refreshes the account into that organization', async () => {
  const session = {
    accessToken: 'initial',
    refreshToken: 'refresh',
    principalId: 'user_one',
    organizationId: null,
  };
  const identity = {
    principalId: 'user_one',
    organizationId: 'unassigned',
    organizations: [
      { id: 'org_one', name: 'One' },
      { id: 'org_two', name: 'Two' },
    ],
  };
  const selected: string[] = [];
  const result = await selectOrganization({
    session,
    identity,
    questions: {
      choose: async (organizations) => {
        expect(organizations).toEqual(identity.organizations);
        return 'org_two';
      },
      create: async () => {
        throw new Error('Unexpected creation');
      },
    },
    refresh: async (previous, organizationId) => {
      selected.push(organizationId);
      return { ...previous, accessToken: 'scoped', organizationId };
    },
    create: async () => {
      throw new Error('Unexpected creation');
    },
  });
  expect(selected).toEqual(['org_two']);
  expect(result.organizationId).toBe('org_two');
});

it('rejects a selected organization outside the verified memberships', async () => {
  await expect(
    selectOrganization({
      session: {
        accessToken: 'initial',
        refreshToken: 'refresh',
        principalId: 'user_one',
        organizationId: null,
      },
      identity: {
        principalId: 'user_one',
        organizationId: 'unassigned',
        organizations: [{ id: 'org_one', name: 'One' }],
      },
      questions: { choose: async () => 'org_other', create: async () => 'Other' },
      refresh: async () => {
        throw new Error('Must not refresh unauthorized selection');
      },
      create: async () => {
        throw new Error('Must not create');
      },
    }),
  ).rejects.toThrow('verified membership');
});
