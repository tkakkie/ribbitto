import test from 'node:test';
import assert from 'node:assert/strict';
import { createStreamProgress } from '../src/stream-progress-projection.ts';

function projection(snapshot: string | null = "42", initialURL = "/events?after=42&want=message", initialApplied?: string) {
  let applied: string | undefined = initialApplied;
  let shown: string | undefined = snapshot ?? undefined;
  let url = initialURL;
  let visible = true;
  const writes: string[] = [];
  const progress = createStreamProgress({
    applied: () => applied, shown: () => shown, url: () => url,
    base: 'https://example.test/', visible: () => visible,
    writeApplied: value => { applied = value; writes.push(`applied:${value}`); },
    writeShown: value => { shown = value; writes.push(`shown:${value}`); },
    writeURL: (next, after) => {
      next.searchParams.set('after', after);
      url = next.pathname + next.search; writes.push(`url:${url}`);
    },
    notify: () => writes.push('notify'),
  });
  return { progress, writes, hide: () => { visible = false; progress.show(); },
    show: () => { visible = true; progress.show(); } };
}

test('compatible projection preserves values, equality writes and notification order', () => {
  for (const row of [
    { name: 'ordinary', id: '51', snapshot: '42' }, { name: 'URL snapshot', id: '51', snapshot: null },
    { name: 'exact large id', id: '9007199254740993', snapshot: '42' },
    { name: 'legacy BigInt syntax', id: '0x33', snapshot: '42' },
  ]) {
    const { progress, writes, hide, show } = projection(row.snapshot);
    progress.connectionCreated('/events?after=42');
    hide();
    if (row.snapshot === null) {
      assert.throws(() => progress.applied(row.id), TypeError, row.name);
      progress.checkpoint(row.id);
      assert.deepEqual(writes, [`url:/events?after=${row.id}&want=message`], row.name);
      continue;
    }
    progress.applied(row.id);
    const url = `/events?after=${row.id}&want=message`;
    assert.deepEqual(writes, [`applied:${row.id}`, `url:${url}`], row.name);
    show();
    assert.deepEqual(writes.slice(2), [`shown:${row.id}`, 'notify'], row.name);
    writes.length = 0;
    progress.applied(row.id);
    assert.deepEqual(writes, [`applied:${row.id}`, `url:${url}`, `shown:${row.id}`, 'notify'], row.name);
    progress.connectionCreated(url);
    assert.equal(progress.snapshot()?.Uc, row.id, row.name);
    writes.length = 0;
    progress.applied('41');
    assert.deepEqual(writes, [], row.name);
    assert.throws(() => progress.applied('bad'), SyntaxError, row.name);
    assert.deepEqual(writes, [], row.name);
  }
});

test('reporting separates connections, raw dispatch, failures, transport and visibility', () => {
  const { progress, writes, hide, show } = projection();
  progress.connectionCreated('/events?after=42');
  hide();
  progress.report({ type: 'frame-dispatched', kind: 'message', lastEventId: '51' });
  progress.report({ type: 'failed', reason: 'apply-exception', id: '51' });
  progress.applied('52');
  progress.checkpoint('60');
  assert.throws(() => progress.checkpoint('bad'), SyntaxError);
  assert.deepEqual(writes, ['applied:52', 'url:/events?after=52&want=message', 'url:/events?after=60&want=message']);
  assert.deepEqual([progress.snapshot()?.Ap, progress.snapshot()?.Sh, progress.snapshot()?.G], ['52', '42', '60']);
  for (const row of [
    { name: 'inherited ephemeral', kind: 'sidebar', id: '60', expected: '51' },
    { name: 'raw malformed', kind: 'message', id: ' 61 ', expected: '51' },
    { name: 'durable checkpoint', kind: 'checkpoint', id: '60', expected: '60' },
  ]) {
    progress.report({ type: 'frame-dispatched', kind: row.kind, lastEventId: row.id });
    assert.equal(progress.snapshot()?.Rc, row.expected, row.name);
  }
  progress.connectionCreated('/events?after=60');
  assert.deepEqual([progress.snapshot()?.Uc, progress.snapshot()?.Rc], ['60', null]);
  progress.report({ type: 'failed', reason: 'history-reapply', id: '52' });
  assert.deepEqual(progress.snapshot()?.F, { reason: 'apply-exception', id: '51' });
  progress.report({ type: 'terminal' });
  show();
  assert.equal(progress.snapshot()?.terminal, false);
  assert.deepEqual(writes.slice(-2), ['shown:52', 'notify']);
});

test('compatible outcomes after terminal retain applied and transport progress', () => {
  const { progress, writes } = projection();
  progress.applied('50');
  progress.report({ type: 'terminal' });
  writes.length = 0;
  progress.applied('51');
  assert.deepEqual(writes, ['applied:51', 'url:/events?after=51&want=message', 'shown:51', 'notify']);
  writes.length = 0;
  progress.checkpoint('52');
  assert.deepEqual(writes, ['url:/events?after=52&want=message']);
});

test('compatible initialization defers BigInt parsing to the legacy outcome boundary', () => {
  for (const value of ['0x10', '007', ' 12', '', 'abc', '-1', '1e3', '9007199254740993']) {
    for (const attribute of ['after', 'shown', 'applied']) {
      const url = '/events?after=' + encodeURIComponent(attribute === 'after' ? value : '0');
      const { progress, writes } = projection(attribute === 'shown' ? value : '0', url,
        attribute === 'applied' ? value : undefined);
      const name = `${attribute}: ${JSON.stringify(value)}`;
      let openError: unknown;
      try { progress.connectionCreated(url); } catch (error) { openError = error; }
      assert.equal(openError, undefined, name);
      const invalid = value === 'abc' || value === '1e3';
      if (invalid) {
        assert.throws(() => progress.applied('9007199254740994'), SyntaxError, name);
        assert.deepEqual(writes, attribute === 'after' ? ['applied:9007199254740994'] : [], name);
      } else {
        progress.applied('9007199254740994');
        assert.deepEqual(writes, ['applied:9007199254740994', 'url:/events?after=9007199254740994',
          'shown:9007199254740994', 'notify'], name);
      }
      const checkpoint = projection(attribute === 'shown' ? value : '0', url,
        attribute === 'applied' ? value : undefined);
      checkpoint.progress.connectionCreated(url);
      if (invalid && attribute === 'after') {
        assert.throws(() => checkpoint.progress.checkpoint('9007199254740994'), SyntaxError, name);
      } else {
        checkpoint.progress.checkpoint('9007199254740994');
        assert.deepEqual(checkpoint.writes, ['url:/events?after=9007199254740994'], name);
      }
    }
  }
  const empty = projection('', '/events?after=0x10', '');
  empty.progress.connectionCreated('/events?after=0x10');
  empty.progress.applied('007');
  assert.deepEqual(empty.writes, ['applied:007', 'shown:007', 'notify']);
  const missing = projection('42', '/events');
  missing.progress.connectionCreated('/events');
  assert.throws(() => missing.progress.checkpoint('51'), TypeError);
  assert.deepEqual(missing.writes, []);
});
