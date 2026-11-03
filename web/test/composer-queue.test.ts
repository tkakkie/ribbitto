import test from 'node:test';
import assert from 'node:assert/strict';
import { createComposerQueue, holdsReload, type PendingItem, type Delivery } from '../src/composer-queue.ts';

type Queue = ReturnType<typeof createComposerQueue>;
const posted = { id: 'message-a', topic: 'topic-a', seq: 9007199254740993n };
function admitted(result: ReturnType<Queue['admit']>): PendingItem {
  if (typeof result === 'string') throw new Error(`expected pending item, got ${result}`);
  return result;
}
function fixture(changed: () => void = () => {}) {
  let clock = 0, key = 0;
  const q = createComposerQueue(() => clock, () => `key-${++key}`, changed);
  return { q, advance: (ms: number) => { clock += ms; } };
}
function failed(q: Queue, status = 500, text = ' raw\r\ntext ') {
  const item = admitted(q.admit(text, 'feed'));
  q.dispatch();
  q.settle(item, status);
  return item;
}

test('admission normalizes limits and defers ordinary submissions', () => {
  for (const row of [
    { name: 'blank', text: ' \u3000\n', quoted: false, ordinary: true },
    { name: 'quote', text: 'body', quoted: true, ordinary: true },
    { name: 'over limit', text: '🙂'.repeat(4001), quoted: false, ordinary: true },
    { name: 'trimmed limit', text: '🙂'.repeat(4000) + ' \r\n', quoted: false, ordinary: false },
    { name: 'CRLF limit', text: 'x\r\n'.repeat(1999) + 'xx \r\n', quoted: false, ordinary: false },
    { name: 'CR limit', text: 'x\r'.repeat(1999) + 'xx \r', quoted: false, ordinary: false },
  ]) {
    for (const busy of [false, true]) {
      const { q } = fixture();
      const result = q.admit(row.text, 'feed', busy, row.quoted);
      if (busy || row.ordinary) assert.equal(result, busy ? 'busy' : 'ordinary', row.name);
      else assert.deepEqual(result, { draftKey: 'key-1', requestKey: 'key-1', state: 'queued', unconfirmed: false, text: row.text, postedMessage: null }, row.name);
    }
    const { q } = fixture();
    admitted(q.admit('earlier', 'feed'));
    const result = q.admit(row.text, 'feed', false, row.quoted);
    assert.equal(typeof result === 'string' ? result : 'queued', row.ordinary ? 'busy' : 'queued', row.name);
  }
});

test('settlement classifies responses and preserves prior uncertainty on refusal', () => {
  for (const row of [
    { name: 'confirmed', status: 200 }, { name: 'conflict', status: 409 },
    { name: 'bad request', status: 400 }, { name: 'missing', status: 404 },
    { name: 'too large', status: 413 }, { name: 'invalid', status: 422 },
    { name: 'rate limited', status: 429 }, { name: 'network', status: 0 },
    { name: 'timeout', status: 408 }, { name: 'unauthorized', status: 401 },
    { name: 'server error', status: 500 }, { name: 'unavailable', status: 503 },
  ]) for (const uncertain of [false, true]) {
    const { q } = fixture();
    if (uncertain) q.restore('restored', 'body');
    const item = admitted(q.admit('body', 'feed')), key = item.requestKey;
    assert.equal(q.dispatch(), item, row.name);
    const notice = q.settle(item, row.status, row.status === 200 ? posted : undefined);
    const refused = [400, 404, 413, 422, 429].includes(row.status);
    assert.deepEqual([item.state, item.unconfirmed, item.postedMessage, notice],
      row.status === 200 ? ['confirmed', false, posted.id, ''] :
      row.status === 409 ? ['failed', false, null, 'repeat'] :
      ['failed', refused ? uncertain : true, null, refused ? (uncertain ? 'stillUnconfirmed' : 'notSent') : 'unconfirmed'], row.name);
    assert.equal(item.text, 'body', row.name);
    assert.equal(item.requestKey === key, row.status !== 409, row.name);
  }
});

test('refused transitions leave the queue and restoration unchanged', () => {
  for (const row of [
    { name: 'queued' }, { name: 'sending' }, { name: 'failed' },
    { name: 'confirmed' }, { name: 'detached' },
  ]) {
    const { q } = fixture();
    const item = admitted(q.admit('body', 'feed'));
    if (row.name !== 'queued' && row.name !== 'detached') q.dispatch();
    if (row.name === 'failed' || row.name === 'confirmed') q.settle(item, row.name === 'failed' ? 400 : 200);
    if (row.name === 'detached') q.remove(item, 'feed');
    const before = { items: q.items.map(i => ({ ...i })), item: { ...item }, restoration: q.restoration };
    if (row.name !== 'sending') assert.equal(q.settle(item, 500), undefined, row.name);
    if (row.name !== 'failed') {
      assert.equal(q.retry(item), false, row.name);
      assert.equal(q.edit(item, '', false), undefined, row.name);
    } else {
      assert.equal(q.edit(item, ' ', false), undefined, row.name);
      assert.equal(q.edit(item, '', true), undefined, row.name);
    }
    if (row.name === 'detached') assert.equal(q.remove(item, 'feed'), undefined, row.name);
    assert.deepEqual({ items: q.items, item, restoration: q.restoration }, before, row.name);
  }
});

test('FIFO pauses unknown outcomes and continues after notSent', () => {
  for (const row of [{ name: 'unknown', status: 500 }, { name: 'not sent', status: 400 }]) {
    const { q } = fixture();
    const a = admitted(q.admit('a', 'feed')), b = admitted(q.admit('b', 'feed'));
    assert.equal(q.dispatch(), a, row.name);
    q.settle(a, row.status);
    assert.equal(q.dispatch(), row.status === 500 ? undefined : b, row.name);
  }
});
test('Retry keeps uncertain items in place and requeues refused items last', () => {
  for (const row of [{ name: 'uncertain', status: 500 }, { name: 'refused', status: 400 }]) {
    const { q } = fixture();
    const a = failed(q, row.status), b = admitted(q.admit('b', 'feed'));
    const identity = [a.text, a.requestKey];
    assert.equal(q.retry(a), true, row.name);
    assert.deepEqual(q.items, row.status === 500 ? [a, b] : [b, a], row.name);
    assert.deepEqual([a.state, a.text, a.requestKey], ['queued', ...identity], row.name);
  }
});
test('409 rotates requestKey while preserving draftKey', () => {
  const { q } = fixture();
  const a = admitted(q.admit('body', 'feed')), draft = a.draftKey;
  q.dispatch();
  q.settle(a, 409);
  assert.deepEqual([a.draftKey, a.requestKey === draft], [draft, false]);
});
test('capacity includes every retained item state and frees removed slots', () => {
  for (const row of [{ name: 'queued' }, { name: 'sending' }, { name: 'failed' }, { name: 'confirmed' }]) {
    const { q } = fixture();
    for (let i = 0; i < 10; i++) {
      const item = admitted(q.admit(`body ${i}`, 'feed'));
      if (row.name !== 'queued') q.dispatch();
      if (row.name === 'failed' || row.name === 'confirmed') q.settle(item, row.name === 'failed' ? 400 : 200, posted);
      // Sending items serialize, so this row includes one sending and nine queued.
    }
    assert.equal(q.admit('eleventh', 'feed', true), 'capacity', row.name);
    if (row.name === 'failed') assert.equal(q.retry(q.items[0]), true, row.name);
    q.remove(q.items[0], 'feed');
    assert.equal(admitted(q.admit('freed slot', 'feed')).state, 'queued', row.name);
  }
});

test('dispatch locks survive removal until the detached request settles', () => {
  const { q } = fixture();
  const a = admitted(q.admit('a', 'feed'));
  assert.equal(q.dispatch(true), undefined);
  assert.equal(q.dispatch(), a);
  q.remove(a, 'feed');
  assert.equal(q.admit(' ', 'feed'), 'busy');
  const b = admitted(q.admit('b', 'feed'));
  assert.equal(q.dispatch(), undefined);
  assert.equal(q.settle(a, 400), 'notSent');
  assert.equal(q.dispatch(), b);
  q.settle(b, 200);
  assert.equal(q.dispatch(), undefined);
});

test('delivery before the response reconciles confirmed identity and routing', () => {
  for (const row of [
    { name: 'stream first', view: { ids: [posted.id] }, removed: true },
    { name: 'unrelated ID', view: { ids: ['other'] }, removed: false },
    { name: 'other topic', view: { ids: [], topic: 'topic-b' }, removed: true },
    { name: 'cursor reached', view: { ids: [], topic: posted.topic, applied: posted.seq }, removed: true },
    { name: 'cursor behind', view: { ids: [], topic: posted.topic, applied: posted.seq - 1n }, removed: false },
    { name: 'feed cursor', view: { ids: [], applied: posted.seq }, removed: false },
    { name: 'missing cursor', view: { ids: [], topic: posted.topic }, removed: false },
  ] satisfies { name: string; view: Delivery; removed: boolean }[]) {
    const { q } = fixture();
    const a = admitted(q.admit('body', 'feed'));
    q.deliver(row.view);
    assert.deepEqual(q.items, [a], row.name);
    q.dispatch();
    q.settle(a, 200, posted, row.view);
    assert.deepEqual(q.items, row.removed ? [] : [a], row.name);
  }
});
test('delivery after the response removes only matching applied messages', () => {
  for (const row of [
    { name: 'response first', view: { ids: [posted.id] }, removed: true },
    { name: 'move from topic', view: { ids: [], topic: posted.topic, moved: { fromTopic: posted.topic, ids: [posted.id] } }, removed: true },
    { name: 'move from elsewhere', view: { ids: [], topic: posted.topic, moved: { fromTopic: 'other', ids: [posted.id] } }, removed: false },
    { name: 'unrelated move', view: { ids: [], topic: posted.topic, moved: { fromTopic: posted.topic, ids: ['other'] } }, removed: false },
    { name: 'feed move', view: { ids: [], moved: { fromTopic: posted.topic, ids: [posted.id] } }, removed: false },
  ] satisfies { name: string; view: Delivery; removed: boolean }[]) {
    const { q } = fixture();
    const a = admitted(q.admit('body', 'feed'));
    q.dispatch(); q.settle(a, 200, posted);
    q.deliver(row.view);
    assert.deepEqual(q.items, row.removed ? [] : [a], row.name);
  }
});

test('Edit returns raw identity and preserves uncertainty across nonempty edits', () => {
  for (const row of [{ name: 'uncertain', uncertain: true }, { name: 'conflict', uncertain: false }]) {
    const { q } = fixture();
    const a = failed(q, row.uncertain ? 500 : 409);
    q.restore('stale', 'stale');
    assert.deepEqual(q.edit(a, '', false), { draftKey: a.draftKey, text: a.text, requestKey: a.requestKey, unconfirmed: row.uncertain }, row.name);
    assert.deepEqual(q.items, [], row.name);
    assert.equal(q.changeText('nonempty edit'), undefined, row.name);
    const b = admitted(q.admit('nonempty edit', 'feed'));
    assert.deepEqual([b.requestKey, b.unconfirmed, q.restoration], [a.requestKey, row.uncertain, null], row.name);
  }
});
test('draft restoration reuses identity only while its snapshot remains valid', () => {
  for (const row of [
    { name: 'unchanged', act: () => {}, reused: true },
    { name: 'nonempty edit', act: q => q.changeText('changed'), reused: false },
    { name: 'empty', act: q => q.changeText(''), reused: false },
    { name: 'matching sent', act: q => q.sent('saved'), reused: false },
    { name: 'unrelated sent', act: q => q.sent('other'), reused: true },
    { name: 'ordinary 200', act: q => q.ordinarySettled(200), reused: false },
    { name: 'ordinary 409', act: q => q.ordinarySettled(409), reused: false },
    { name: 'ordinary refused', act: q => q.ordinarySettled(422), reused: true },
  ] satisfies { name: string; act: (q: Queue) => void; reused: boolean }[]) {
    const { q } = fixture(); q.restore('saved', 'body'); row.act(q);
    const a = admitted(q.admit('body', 'feed'));
    assert.deepEqual([a.requestKey === 'saved', a.unconfirmed], [row.reused, row.reused], row.name);
    assert.equal(q.restoration, null, row.name);
  }
});
test('Edit restoration releases on clearing, sending or confirmation', () => {
  for (const row of [{ name: 'empty' }, { name: 'sent' }, { name: 'confirm' }]) {
    const { q } = fixture(); const a = failed(q); q.edit(a, '', false);
    const fresh = row.name === 'empty' ? q.changeText('') : row.name === 'sent' ? q.sent(a.requestKey) : q.ordinarySettled(200);
    assert.equal(typeof fresh, 'string', row.name);
    assert.notEqual(admitted(q.admit('replacement', 'feed')).requestKey, a.requestKey, row.name);
  }
});
test('Remove holds match raw text and place until expiry or release', () => {
  for (const row of [
    { name: 'same raw/place', act: () => {}, place: 'feed', text: ' raw\r\ntext ', reused: true },
    { name: 'different place', act: () => {}, place: 'topic', text: ' raw\r\ntext ', reused: false },
    { name: 'different raw', act: () => {}, place: 'feed', text: 'raw\ntext', reused: false },
    { name: 'just before expiry', act: (_, advance) => advance(299999), place: 'feed', text: ' raw\r\ntext ', reused: true },
    { name: 'expiry boundary', act: (_, advance) => advance(300000), place: 'feed', text: ' raw\r\ntext ', reused: false },
    { name: 'expiry callback', act: (q, advance) => { advance(300000); q.expire(); }, place: 'feed', text: ' raw\r\ntext ', reused: false },
    { name: 'pagehide', act: q => q.pagehide(), place: 'feed', text: ' raw\r\ntext ', reused: false },
    { name: 'sent', act: (q, _, a) => q.sent(a.requestKey), place: 'feed', text: ' raw\r\ntext ', reused: false },
    { name: 'unrelated sent', act: q => q.sent('other'), place: 'feed', text: ' raw\r\ntext ', reused: true },
    { name: 'restored precedence', act: q => q.restore('saved', ' raw\r\ntext '), place: 'feed', text: ' raw\r\ntext ', reused: false },
  ] satisfies { name: string; act: (q: Queue, advance: (ms: number) => void, a: PendingItem) => void; place: string; text: string; reused: boolean }[]) {
    const { q, advance } = fixture(), a = failed(q);
    assert.equal(q.remove(a, 'feed'), a.draftKey, row.name);
    row.act(q, advance, a);
    const b = admitted(q.admit(row.text, row.place));
    assert.equal(b.requestKey === a.requestKey, row.reused, row.name);
    assert.equal(b.unconfirmed, row.reused || row.name === 'restored precedence', row.name);
    if (row.name === 'restored precedence') {
      assert.equal(b.requestKey, 'saved', row.name);
      const next = admitted(q.admit(row.text, row.place));
      assert.notEqual(next.requestKey, a.requestKey, row.name);
      assert.equal(next.unconfirmed, false, row.name);
    }
    q.remove(b, row.place); q.pagehide();
    assert.notEqual(admitted(q.admit(row.text, row.place)).requestKey, a.requestKey, row.name);
  }
});
test('Remove does not hold confirmed or refused identities', () => {
  for (const row of [{ name: 'confirmed', status: 200 }, { name: 'refused', status: 400 }, { name: 'conflict', status: 409 }]) {
    const { q } = fixture(), a = failed(q, row.status);
    q.remove(a, 'feed');
    assert.equal(admitted(q.admit(a.text, 'feed')).unconfirmed, false, row.name);
  }
});

test('reload is held by any unresolved item regardless of uncertainty', () => {
  for (const row of [
    { name: 'queued', state: 'queued', held: true }, { name: 'sending', state: 'sending', held: true },
    { name: 'failed', state: 'failed', held: true }, { name: 'confirmed', state: 'confirmed', held: false },
  ] satisfies { name: string; state: PendingItem['state']; held: boolean }[]) {
    for (const unconfirmed of [false, true]) {
      assert.equal(holdsReload([{ draftKey: 'draft', requestKey: 'request', text: 'body', postedMessage: null, state: row.state, unconfirmed }]), row.held, row.name);
    }
  }
  assert.equal(holdsReload([]), false, 'empty queue');
});
test('notifications synchronously publish reload release and final state', () => {
  for (const row of [{ name: 'remove' }, { name: 'edit' }, { name: 'confirm' }, { name: 'delivery' }, { name: 'delayed delivery' }]) {
    let projection: unknown;
    const { q } = fixture(() => { projection = [holdsReload(q.items), q.items.map(i => i.state)]; });
    const a = admitted(q.admit('body', 'feed'));
    assert.deepEqual(projection, [true, ['queued']], row.name);
    q.dispatch();
    if (row.name === 'remove' || row.name === 'edit') q.settle(a, 500);
    if (row.name === 'remove') q.remove(a, 'feed');
    if (row.name === 'edit') q.edit(a, '', false);
    if (row.name === 'confirm') q.settle(a, 200, posted);
    if (row.name === 'delivery') q.settle(a, 200, posted, { ids: [posted.id] });
    if (row.name === 'delayed delivery') {
      q.settle(a, 200, posted);
      assert.deepEqual(projection, [false, ['confirmed']], row.name);
      q.deliver({ ids: [posted.id] });
    }
    assert.deepEqual(projection, [false, row.name === 'confirm' ? ['confirmed'] : []], row.name);
  }
});

test('ordinary outcomes after Edit preserve identity and classify uncertainty', () => {
  for (const row of [
    { name: 'bad request', status: 400, uncertain: false }, { name: 'rate limited', status: 429, uncertain: false },
    { name: 'network', status: 0, uncertain: true }, { name: 'server error', status: 500, uncertain: true },
  ]) {
    const { q } = fixture(), a = failed(q, 400);
    q.edit(a, '', false); q.ordinarySettled(row.status);
    assert.deepEqual(q.restoration, { key: a.requestKey, text: null, unconfirmed: row.uncertain }, row.name);
  }
});
test('notifications expose Retry and preserve a hold while another item remains', () => {
  let projection: unknown;
  const { q } = fixture(() => { projection = [holdsReload(q.items), q.items.map(i => i.state)]; });
  const a = failed(q, 400), b = admitted(q.admit('b', 'feed'));
  q.retry(a);
  assert.deepEqual(projection, [true, ['queued', 'queued']]);
  q.remove(a, 'feed');
  assert.deepEqual(projection, [true, ['queued']]);
  q.dispatch(); q.settle(b, 200, posted);
  assert.deepEqual(projection, [false, ['confirmed']]);
});

test('Remove after 409 rotation reports original draftKey and holds rotated requestKey', () => {
  const { q } = fixture();
  const text = ' raw\r\ntext ', place = 'feed';
  const item = admitted(q.admit(text, place)), draftKey = item.draftKey;
  assert.equal(q.dispatch(), item);
  assert.equal(q.settle(item, 409), 'repeat');
  const requestKey = item.requestKey;
  assert.notEqual(requestKey, draftKey);
  assert.equal(q.retry(item), true);
  assert.equal(q.dispatch(), item);
  assert.equal(q.settle(item, 500), 'unconfirmed');
  assert.equal(q.remove(item, place), draftKey);
  const resend = admitted(q.admit(text, place));
  assert.equal(resend.requestKey, requestKey);
  assert.equal(resend.unconfirmed, true);
});

test('released restoration keys are available before ordinary or busy admission', () => {
  for (const row of [{ name: 'ordinary', busy: false }, { name: 'busy', busy: true }]) {
    const { q } = fixture();
    q.restore('saved', 'body');
    const fresh = q.changeText('');
    assert.equal(fresh, 'key-1', row.name);
    assert.equal(q.admit('', 'feed', row.busy), row.busy ? 'busy' : 'ordinary', row.name);
    assert.equal(q.restoration, null, row.name);
  }
});

test('restoration snapshots isolate callers and retain their original uncertainty', () => {
  const { q } = fixture(), item = failed(q, 400);
  q.edit(item, '', false);
  const before = q.restoration!, changed: { key: string; text: string | null; unconfirmed: boolean } = q.restoration!;
  changed.key = 'other';
  changed.text = 'other';
  changed.unconfirmed = true;
  assert.deepEqual(q.restoration, { key: item.requestKey, text: null, unconfirmed: false });
  q.ordinarySettled(500);
  assert.equal(before.unconfirmed, false);
  assert.equal(q.restoration!.unconfirmed, true);
});

test('ordinary missing and oversized responses preserve prior uncertainty', () => {
  for (const row of [{ name: 'missing', status: 404 }, { name: 'oversized', status: 413 }]) {
    for (const uncertain of [false, true]) {
      const { q } = fixture(), item = failed(q, uncertain ? 500 : 400);
      q.edit(item, '', false);
      assert.equal(q.ordinarySettled(row.status), undefined, row.name);
      assert.deepEqual(q.restoration, { key: item.requestKey, text: null, unconfirmed: uncertain }, row.name);
    }
  }
});

test('response projection precedes completion in either delivery order', () => {
  for (const row of [{ name: 'stream first', streamFirst: true }, { name: 'response first', streamFirst: false }]) {
    let projection: unknown;
    const { q } = fixture(() => { projection = q.items.map(item => [item.state, item.postedMessage]); });
    const item = admitted(q.admit('body', 'feed'));
    assert.equal(q.next(), item, row.name);
    assert.equal(item.state, 'queued', row.name);
    assert.equal(q.next(true), undefined, row.name);
    q.dispatch();
    q.response(item, posted.id);
    assert.deepEqual(projection, [['sending', posted.id]], row.name);
    q.route(item, posted, { ids: row.streamFirst ? [posted.id] : [] });
    assert.equal(item.state, 'sending', row.name);
    assert.equal(item.postedMessage, posted.id, row.name);
    assert.deepEqual(q.items, row.streamFirst ? [] : [item], row.name);
    assert.equal(q.next(), undefined, row.name);
    q.settle(item, 200);
    assert.equal(item.state, 'confirmed', row.name);
    assert.equal(item.postedMessage, posted.id, row.name);
    q.deliver({ ids: [posted.id] });
    assert.deepEqual(q.items, [], row.name);
  }
});

test('Remove timeout clears only its own hold with a frozen clock', () => {
  const { q } = fixture(), first = failed(q);
  q.remove(first, 'feed');
  const second = admitted(q.admit(first.text, 'feed'));
  q.remove(second, 'feed');
  q.expire(first);
  const third = admitted(q.admit(first.text, 'feed'));
  assert.equal(third.requestKey, first.requestKey);
  q.remove(third, 'feed');
  q.expire(third);
  assert.notEqual(admitted(q.admit(first.text, 'feed')).requestKey, first.requestKey);
});

test('Remove holds survive unrelated admissions and expire before a later resend', () => {
  const { q, advance } = fixture();
  const first = failed(q, 500, 'msg 1');
  q.remove(first, 'feed');
  advance(200000);
  q.admit('msg 2', 'feed');
  const resend = admitted(q.admit('msg 1', 'feed'));
  assert.deepEqual([resend.requestKey, resend.unconfirmed], [first.requestKey, true]);
  q.remove(resend, 'feed');
  advance(300000);
  const expired = admitted(q.admit('msg 1', 'feed'));
  assert.notEqual(expired.requestKey, first.requestKey);
  assert.equal(expired.unconfirmed, false);
});

test('409 notifications expose the rotated request key synchronously', () => {
  let projection: unknown;
  const { q } = fixture(() => { projection = q.items.map(item => [item.state, item.requestKey]); });
  const item = admitted(q.admit('hello', 'feed'));
  q.dispatch();
  q.settle(item, 409);
  assert.deepEqual(projection, [['failed', 'key-2']]);
});
