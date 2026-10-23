import { expect, it, vi } from 'vitest';
import { printJson } from './tool-commands.js';

it('prints command output with control and bidi characters escaped', () => {
  const write = vi.spyOn(process.stdout, 'write').mockImplementation(() => true);
  try {
    printJson({ description: 'a\u009bb\u202ec\u001bd' });
    expect(write).toHaveBeenCalledWith('{\n  "description": "a\\u009bb\\u202ec\\u001bd"\n}\n');
  } finally {
    write.mockRestore();
  }
});
