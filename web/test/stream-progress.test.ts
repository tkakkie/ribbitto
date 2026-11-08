import test from 'node:test';
import assert from 'node:assert/strict';
import { createProgress, advanceProgress, type Progress, type ProgressInput, type FailureReason } from '../src/stream-progress.ts';
import { advancesCursor } from '../src/stream-cursor.ts';

const modes = ['compatible', 'strict'] as const;
const large = '9007199254740993';
const reasons: FailureReason[] = ['apply-exception', 'apply-verification', 'decode-missing-item',
  'decode-missing-id', 'decode-invalid-id', 'decode-non-monotonic', 'history-reapply'];
function cursors(p: Progress): string[] { return [p.P0, p.Sh, p.Ap, p.G]; }
function run(p: Progress, inputs: ProgressInput[]): Progress {
  return inputs.reduce(advanceProgress, p);
}

test('durability uses kind; malformed ids and inherited ephemeral ids leave Rc alone', () => {
  for (const mode of modes) {
    let p = advanceProgress(createProgress('0', mode, true), { type: 'frame-dispatched', kind: 'message', lastEventId: large });
    for (const kind of ['unread', 'sidebar', 'presence', 'typing', 'pins', 'owner-reset']) {
      assert.deepEqual(advanceProgress(p, { type: 'frame-dispatched', kind, lastEventId: '9007199254740994' }), p, kind);
    }
    for (const id of ['', '-1', '1.5', 'bad', ' 1 ', '0x10', '1\n']) {
      for (const kind of ['message', 'messages-moved', 'checkpoint', 'reset']) {
        assert.deepEqual(advanceProgress(p, { type: 'frame-dispatched', kind, lastEventId: id }), p, `${kind}: ${id}`);
      }
    }
    p = advanceProgress(p, { type: 'connection-created', after: '0' });
    assert.equal(p.Rc, null);
    assert.equal(p.Uc, '0');
    assert.throws(() => advanceProgress(p, { type: 'connection-created', after: large }), /invalid/);
    assert.throws(() => createProgress('bad', mode, true), /invalid/);
  }
});

test('application, no-op, checkpoints and visibility retain exact monotonic cursors', () => {
  for (const mode of modes) {
    let p = createProgress(large, mode, false);
    assert.deepEqual([...cursors(p), p.Uc], [large, large, large, large, large]);
    p = advanceProgress(p, { type: 'connection-created', after: large });
    assert.equal(p.Uc, large);
    assert.equal(p.Rc, null);
    p = advanceProgress(p, { type: 'checkpoint', id: '9007199254740995' });
    assert.deepEqual(cursors(p), [large, large, large, '9007199254740995']);
    p = advanceProgress(p, { type: 'contract-no-op', id: '9007199254740994' });
    assert.deepEqual(cursors(p), [large, large, '9007199254740994', '9007199254740995']);
    p = advanceProgress(p, { type: 'visibility-changed', visible: true });
    assert.equal(p.Sh, '9007199254740994');
    for (const id of ['9007199254740994', large]) {
      assert.deepEqual(cursors(advanceProgress(p, { type: 'applied', id })), cursors(p), id);
    }
    p = advanceProgress(p, { type: 'applied', id: '9007199254740996' });
    assert.deepEqual(cursors(p), [large, '9007199254740996', '9007199254740996', '9007199254740996']);
  }
});

for (const mode of modes) {
  for (const type of ['applied', 'contract-no-op', 'checkpoint', 'terminal'] as const) {
    test(`${mode}: stale ${type} id above 2^53 cannot lower cursors`, () => {
      const high = '9007199254740997';
      const low = '9007199254740996';
      const before = createProgress(high, mode, true);
      const after = type === 'terminal'
        ? run(before, [{ type: 'frame-dispatched', kind: 'reset', lastEventId: low }, { type }])
        : advanceProgress(before, { type, id: low });
      assert.deepEqual(cursors(after), cursors(before));
      if (type === 'terminal') {
        assert.equal(after.terminal, true);
        assert.equal(after.Rc, low);
        assert.deepEqual(cursors(run(after, [{ type: 'applied', id: '9007199254740999' },
          { type: 'checkpoint', id: '9007199254740999' }])), cursors(before));
      }
    });
  }
}

test('first failure is exact and permanent; strict stops even with unknown id', () => {
  for (const mode of modes) {
    for (const reason of reasons) {
      for (const id of [large, null]) {
        const initial = createProgress('0', mode, true);
        const failed = advanceProgress(initial, { type: 'failed', reason, id });
        assert.deepEqual(cursors(failed), cursors(initial), `${mode}/${reason}/${id}`);
        const later = run(failed, [
          { type: 'failed', reason: 'apply-exception', id: '9999999999999999' },
          { type: 'checkpoint', id: '9007199254740994' },
          { type: 'applied', id: large },
          { type: 'contract-no-op', id: '9007199254740995' },
        ]);
        assert.deepEqual(later.F, { reason, id });
        assert.deepEqual(cursors(later), mode === 'strict' ? ['0', '0', '0', '0']
          : ['0', '9007199254740995', '9007199254740995', '9007199254740995']);
      }
    }
  }
});

for (const mode of modes) {
  test(`${mode}: malformed first failure ids become null and remain first`, () => {
    for (const id of ['bad', ' ', '', '-1', '1.5', '0x10', '0', large]) {
      const expected = ['0', large].includes(id) ? id : null;
      const initial = createProgress('0', mode, true);
      const failed = advanceProgress(initial, { type: 'failed', reason: 'decode-invalid-id', id });
      assert.deepEqual(failed.F, { reason: 'decode-invalid-id', id: expected }, JSON.stringify(id));
      assert.deepEqual(advanceProgress(failed, { type: 'failed', reason: 'apply-exception', id: large }).F, failed.F);
      const outcomes: ProgressInput[] = [
        { type: 'applied', id: large }, { type: 'contract-no-op', id: large }, { type: 'checkpoint', id: large },
      ];
      const unknown = advanceProgress(initial, { type: 'failed', reason: 'decode-invalid-id', id: null });
      assert.deepEqual(cursors(run(failed, outcomes)), cursors(run(unknown, outcomes)));
    }
  });
}

test('history re-application failure cannot roll receipt progress back', () => {
  for (const mode of modes) {
    const received = advanceProgress(createProgress('0', mode, false), { type: 'applied', id: large });
    const failed = advanceProgress(received, { type: 'failed', reason: 'history-reapply', id: large });
    assert.deepEqual(cursors(failed), cursors(received));
    assert.deepEqual(failed.F, { reason: 'history-reapply', id: large });
    assert.equal(advanceProgress(failed, { type: 'visibility-changed', visible: true }).Sh, large);
  }
});

test('reset is terminal regardless of id and still permits shown catch-up', () => {
  for (const mode of modes) {
    for (const id of ['9007199254740994', large, '0', '']) {
      const before = run(createProgress('0', mode, false), [
        { type: 'frame-dispatched', kind: 'message', lastEventId: large }, { type: 'applied', id: large },
      ]);
      const reset = run(before, [{ type: 'frame-dispatched', kind: 'reset', lastEventId: id }, { type: 'terminal' }]);
      assert.deepEqual(cursors(reset), cursors(before), id);
      assert.equal(reset.Rc, id || large);
      assert.equal(reset.F, null);
      if (id === '9007199254740994') assert.equal(BigInt(reset.Rc!) > BigInt(reset.G), true);
      const later = run(reset, [{ type: 'checkpoint', id: '9999999999999999' },
        { type: 'applied', id: '9999999999999999' }, { type: 'contract-no-op', id: '9999999999999999' }]);
      assert.deepEqual(cursors(later), cursors(before));
      assert.equal(advanceProgress(later, { type: 'visibility-changed', visible: true }).Sh, large);
    }
  }
});

// This reference follows resume/progress/showApplied, independently of the reducer.
function legacy(p: { Ap: string; Sh: string; G: string; visible: boolean; terminal: boolean }, input: ProgressInput): void {
  if (input.type === 'visibility-changed') {
    p.visible = input.visible;
    if (p.visible) p.Sh = p.Ap;
  } else if (!p.terminal) {
    if (input.type === 'terminal') p.terminal = true;
    if ((input.type === 'applied' || input.type === 'contract-no-op') && advancesCursor(input.id, p.Ap)) {
      p.Ap = input.id;
      if (advancesCursor(input.id, p.G)) p.G = input.id;
      if (p.visible) p.Sh = p.Ap;
    }
    if (input.type === 'checkpoint' && advancesCursor(input.id, p.G)) p.G = input.id;
  }
}

function randomSource(seed: number): () => number {
  let value = seed;
  return () => {
    value += 0x6D2B79F5;
    let t = Math.imul(value ^ value >>> 15, 1 | value);
    t ^= t + Math.imul(t ^ t >>> 7, 61 | t);
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}

test('seeded histories preserve page, connection and policy invariants in both modes', () => {
  const seed = 2065;
  const random = randomSource(seed);
  for (const failureFree of [true, false]) {
    for (let trial = 0; trial < 40; trial++) {
      let states = modes.map(mode => createProgress(large, mode, true));
      const reference = { Ap: large, Sh: large, G: large, visible: true, terminal: false };
      let sequence = BigInt(large);
      const resetAt = 60 + Math.floor(random() * 30);
      for (let step = 0; step < 100; step++) {
        const choice = Math.floor(random() * 10);
        const offset = random() < 0.5 ? -BigInt(Math.floor(random() * 4)) : 1n;
        const candidate = sequence + offset;
        const id = (candidate < 0n ? 0n : candidate).toString();
        const batch: ProgressInput[] = [];
        if (step === resetAt) {
          batch.push({ type: 'frame-dispatched', kind: 'reset', lastEventId: id }, { type: 'terminal' });
        } else if (choice === 0) {
          // Both modes receive the same after, bounded by the smaller safe cursor.
          const floor = BigInt(states[0].P0);
          const ceiling = states.map(p => BigInt(p.G)).reduce((a, b) => a < b ? a : b);
          const pick = Math.floor(random() * 3);
          const after = pick === 0 ? ceiling : pick === 1
            ? floor + BigInt(Math.floor(random() * Number(ceiling - floor + 1n))) : 0n;
          batch.push({ type: 'connection-created', after: after.toString() });
        } else if (choice === 1) {
          batch.push({ type: 'visibility-changed', visible: random() > 0.5 });
        } else if (choice === 2) {
          batch.push({ type: 'frame-dispatched', kind: ['unread', 'sidebar', 'presence', 'typing', 'pins'][step % 5], lastEventId: sequence.toString() });
        } else if (choice === 3) {
          batch.push({ type: 'frame-dispatched', kind: 'message', lastEventId: '' });
          if (!failureFree) batch.push({ type: 'failed', reason: 'decode-missing-id', id: null });
        } else {
          if (BigInt(id) > sequence) sequence = BigInt(id);
          batch.push({ type: 'frame-dispatched', kind: choice === 4 ? 'checkpoint' : 'messages-moved', lastEventId: id });
          if (!failureFree && choice === 5) {
            batch.push({ type: 'failed', reason: reasons[step % (reasons.length - 1)], id });
          } else {
            batch.push({ type: choice === 4 ? 'checkpoint' : choice === 6 ? 'contract-no-op' : 'applied', id });
            if (!failureFree && choice === 7) batch.push({ type: 'failed', reason: 'history-reapply', id });
          }
        }
        for (const input of batch) {
          const before = states;
          states = states.map(p => advanceProgress(p, input));
          legacy(reference, input);
          for (let index = 0; index < states.length; index++) {
            const p = states[index];
            const context = `seed=${seed} failureFree=${failureFree} trial=${trial} step=${step} mode=${p.mode} input=${input.type}`;
            const previous = before[index];
            assert.equal(cursors(p).every((v, i) => BigInt(v) >= BigInt(cursors(previous)[i])), true, context);
            if (previous.F) assert.deepEqual(p.F, previous.F, context);
            if (previous.terminal || (previous.mode === 'strict' && previous.F !== null)) {
              assert.equal(p.Ap, previous.Ap, context);
              assert.equal(p.G, previous.G, context);
            }
            const values = cursors(p).map(BigInt);
            assert.equal(values.every((v, i) => i === 0 || values[i - 1] <= v), true, context);
            assert.equal(BigInt(p.Uc) <= BigInt(p.G), true, context);
            if (input.type === 'connection-created') {
              assert.equal(p.Uc, input.after, context);
              assert.equal(p.Rc, null, context);
            }
            assert.equal(p.P0, large, context);
            if (p.visible) assert.equal(p.Sh, p.Ap, context);
            if (input.type === 'checkpoint' || input.type === 'frame-dispatched') {
              assert.equal(p.Ap, before[index].Ap, context);
              assert.equal(p.Sh, before[index].Sh, context);
            }
          }
          // Rc may temporarily exceed G between dispatch and its synchronous outcome.
        }
        for (const p of states) {
          const context = `seed=${seed} trial=${trial} step=${step} mode=${p.mode}`;
          if (failureFree && !p.terminal && p.Rc !== null) assert.equal(BigInt(p.Rc) <= BigInt(p.G), true, context);
          if (p.mode === 'compatible') assert.deepEqual([p.Ap, p.Sh, p.G], [reference.Ap, reference.Sh, reference.G], context);
        }
      }
    }
  }
});
