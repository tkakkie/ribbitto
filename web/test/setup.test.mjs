import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

test('discovers harnesses, strips types and imports relative .ts modules', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'ribbitto-unit-'));
  try {
    await writeFile(join(dir, 'value.ts'), 'export const value: number = 42;\n');
    await writeFile(join(dir, 'logic.ts'), `
      import { value } from './value.ts';
      const result: { value: number; state: 'ready' } = { value, state: 'ready' as const };
      export default result;
    `);
    const { default: result } = await import(pathToFileURL(join(dir, 'logic.ts')).href);
    assert.deepEqual(result, { value: 42, state: 'ready' });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
