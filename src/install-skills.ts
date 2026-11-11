import { join } from 'node:path';
import { z } from 'zod';
import { bundledSkills, skillLicense, skillSource } from './skill-bundle.js';
import { planManagedFile, regularText, saveFileChange } from './managed-files.js';
import type { AgentConfig } from './agents.js';

const manifestSchema = z.object({ sha256: z.string().regex(/^[a-f0-9]{64}$/u) });
async function skillChanges(root: string, skill: (typeof bundledSkills)[number]) {
  const directory = join(root, skill.name);
  const path = join(directory, '.durin-install.json');
  const raw = await regularText(path);
  const previous = raw === null ? undefined : manifestSchema.parse(JSON.parse(raw)).sha256;
  const content = await planManagedFile(join(directory, 'SKILL.md'), skill.content, previous);
  const license = await planManagedFile(join(directory, 'LICENSE'), skillLicense);
  const manifest = JSON.stringify({ ...skillSource, sha256: skill.sha256 }, null, 2) + '\n';
  return [content, license, { path, before: raw, after: manifest }] as const;
}
export async function installSkills(agent: AgentConfig): Promise<number> {
  if (!agent.skillsDirectory) return 0;
  const root = agent.skillsDirectory;
  const changes = await Promise.all(bundledSkills.map((skill) => skillChanges(root, skill)));
  // Validate every destination before replacing any skill.
  await Promise.all(
    changes.map(async ([content, license, manifest]) => {
      await saveFileChange(content);
      await saveFileChange(license);
      await saveFileChange(manifest);
    }),
  );
  return bundledSkills.length;
}
