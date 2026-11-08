import test from 'node:test';
import assert from 'node:assert/strict';
import { composerSubmitMode, createReplyAttempt } from '../src/composer.ts';
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

test('unconfirmed reply retries keep the native key through target changes and cancellation', () => {
  for (const row of [
    { name: 'same target', target: 'original', status: 500, native: true },
    { name: 'changed target', target: 'another', status: 0, native: true },
    { name: 'cancelled target', target: '', status: 503, native: true },
    { name: 'refused before writing', target: '', status: 422, native: false },
    { name: 'confirmed send', target: '', status: 200, native: false },
    { name: 'confirmed conflict', target: '', status: 409, native: false },
  ]) {
    const attempt = createReplyAttempt();
    const queue = createComposerQueue(() => 0, () => 'new-key', () => {});
    const requests: { key: string; target: string; body: string }[] = [];
    const post = (target: string, status: number) => {
      const native = attempt.native(Boolean(target), 'original-key');
      const admitted = queue.admit('Answer', 'feed', false, native);
      if (admitted === 'ordinary') requests.push({ key: 'original-key', target, body: 'Answer' });
      attempt.settle(status, 'original-key', Boolean(target));
      return native;
    };
    assert.equal(post('original', row.status), true, row.name);
    assert.equal(attempt.native(Boolean(row.target), 'different-key'), Boolean(row.target), row.name);
    assert.equal(post(row.target, 409), row.native, row.name);
    if (row.native) {
      assert.deepEqual(requests, [
        { key: 'original-key', target: 'original', body: 'Answer' },
        { key: 'original-key', target: row.target, body: 'Answer' },
      ], row.name);
      assert.deepEqual(queue.items, [], row.name);
    }
    assert.equal(attempt.native(false, 'original-key'), false, row.name);
  }
});
