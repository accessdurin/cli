#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { cp, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';

const directory = resolve(process.argv[2] ?? 'release');
const { version } = JSON.parse(await readFile('package.json', 'utf8'));
const name = `durin-cli-${version}.tgz`;
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const sha256 = digest(await readFile(join(directory, name)));
const formula = `class Durin < Formula\n  desc "Organization tools and coding-agent setup through Durin"\n  homepage "https://github.com/accessdurin/cli"\n  url "https://github.com/accessdurin/cli/releases/download/v${version}/${name}"\n  sha256 "${sha256}"\n  license "MIT"\n\n  depends_on "node@24"\n\n  def install\n    system "npm", "install", *std_npm_args, "--ignore-scripts"\n    bin.write_env_script libexec/"bin/durin", PATH: "#{Formula["node@24"].opt_bin}:$PATH"\n  end\n\n  test do\n    assert_match "durin", shell_output("#{bin}/durin --help")\n  end\nend\n`;
await writeFile(join(directory, 'durin.rb'), formula);
if (process.argv.includes('--write-formula')) {
  await mkdir('Formula', { recursive: true });
  await writeFile('Formula/durin.rb', formula);
}
if (
  process.argv.includes('--check-formula') &&
  (await readFile('Formula/durin.rb', 'utf8')) !== formula
) {
  throw new Error('Homebrew formula differs from this release. Regenerate it before tagging.');
}
await cp('packaging/install-windows.ps1', join(directory, 'install-windows.ps1'));
const names = (await readdir(directory))
  .filter((file) => !['SHA256SUMS', 'SHA256SUMS.asc'].includes(file))
  .toSorted();
const checksums = await Promise.all(
  names.map(async (file) => `${digest(await readFile(join(directory, file)))}  ${file}`),
);
await writeFile(join(directory, 'SHA256SUMS'), checksums.join('\n') + '\n');
console.log(`Generated Homebrew formula and checksums for ${names.length} assets.`);
