import test from 'node:test';
import assert from 'node:assert/strict';
import { composerSubmitMode } from '../src/composer.ts';
import { createComposerQueue } from '../src/composer-queue.ts';

test('reply cancellation leaves the draft outside the posting queue', () => {
  const queue = createComposerQueue(() => 0, () => 'key', () => {});
  let prepared = false;
  const mode = composerSubmitMode('clear_reply', false, true);
  if (mode !== 'cancel') queue.admit('Draft answer', 'feed', false, mode === 'target', () => { prepared = true; });
  assert.equal(mode, 'cancel');
  assert.equal(prepared, false);
  assert.deepEqual(queue.items, []);
  assert.equal(queue.dispatch(), undefined);
});

test('reply sends keep their target on the ordinary form', () => {
  const queue = createComposerQueue(() => 0, () => 'key', () => {});
  let prepared = false;
  const mode = composerSubmitMode(undefined, false, true);
  assert.equal(mode, 'target');
  assert.equal(queue.admit('Draft answer', 'feed', false, mode === 'target', () => { prepared = true; }), 'ordinary');
  assert.equal(prepared, false);
  assert.deepEqual(queue.items, []);
});
