import test from 'node:test';
import assert from 'node:assert/strict';
import { createPageLifecycle } from '../src/lifecycle.ts';

test('page lifecycle: phase order and every pageshow survive repeated freeze and thaw until disposal', () => {
  const cases = [
    { name: 'initial pageshow and ordinary navigation', events: ['show', 'hide'], want: ['start', 'show:false', 'dispose', 'abort'] },
    { name: 'repeated freeze and thaw', events: ['show', 'freeze', 'thaw', 'freeze', 'thaw', 'hide'], want: ['start', 'show:false', 'freeze', 'thaw', 'show:true', 'freeze', 'thaw', 'show:true', 'dispose', 'abort'] },
    { name: 'dispose after freeze', events: ['freeze', 'hide', 'thaw', 'show', 'hide'], want: ['start', 'freeze', 'dispose', 'abort'] },
    { name: 'early disposal is final and idempotent', events: ['dispose', 'dispose', 'show', 'freeze'], want: ['start', 'dispose', 'abort'] },
  ];
  for (const row of cases) {
    const trace: string[] = [];
    const lifecycle = createPageLifecycle({
      phase: phase => trace.push(phase),
      pageshow: persisted => trace.push(`show:${persisted}`),
    });
    lifecycle.signal.addEventListener('abort', () => trace.push('abort'));
    for (const event of row.events) {
      if (event === 'dispose') lifecycle.dispose();
      else if (event === 'hide' || event === 'freeze') lifecycle.pagehide(event === 'freeze');
      else lifecycle.pageshow(event === 'thaw');
    }
    assert.deepEqual(trace, row.want, row.name);
    assert.equal(lifecycle.signal.aborted, true, row.name);
  }
});

test('page lifecycle: instances have independent listener lifetimes', () => {
  const callbacks = { phase: () => {}, pageshow: () => {} };
  const first = createPageLifecycle(callbacks);
  const second = createPageLifecycle(callbacks);
  first.dispose();
  assert.equal(first.signal.aborted, true);
  assert.equal(second.signal.aborted, false);
  second.dispose();
});

test('page lifecycle: disposal aborts listeners even when cleanup throws', () => {
  const lifecycle = createPageLifecycle({
    phase: phase => { if (phase === 'dispose') throw new Error('cleanup failed'); },
    pageshow: () => {},
  });
  assert.throws(() => lifecycle.dispose(), /cleanup failed/);
  assert.equal(lifecycle.signal.aborted, true);
  lifecycle.dispose();
});
