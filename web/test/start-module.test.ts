import test from 'node:test';
import assert from 'node:assert/strict';
import { startModule } from '../src/start-module.ts';
test('start failures are reported unchanged and later starts continue', () => {
  const error = new Error('start failed'), output: unknown[] = [];
  startModule(() => { throw error; }, error => output.push(error));
  startModule(() => output.push('later'), error => output.push(error));
  assert.deepEqual(output, [error, 'later']);
});
