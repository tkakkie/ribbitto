import test from 'node:test';
import assert from 'node:assert/strict';
import { createComposerQueue, holdsReload } from '../src/composer-queue.ts';

const posted = { id: 'message-a', topic: 'topic-a', seq: 9007199254740993n };
function fixture(changed = () => {}) {
  let clock = 0, key = 0;
  const q = createComposerQueue(() => clock, () => `key-${++key}`, changed);
  return { q, advance: ms => { clock += ms; } };
}
function failed(q, status = 500, text = ' raw\r\ntext ') {
  const item = q.admit(text, 'feed');
  q.dispatch();
  q.settle(item, status);
  return item;
}

for (const [name, text, quoted, ordinary] of [
  ['blank', ' \u3000\n', false, true], ['quote', 'body', true, true],
  ['over limit', '🙂'.repeat(4001), false, true],
  ['trimmed limit', '🙂'.repeat(4000) + ' \r\n', false, false],
  ['CRLF limit', 'x\r\n'.repeat(1999) + 'xx \r\n', false, false],
  ['CR limit', 'x\r'.repeat(1999) + 'xx \r', false, false],
]) test(`admission: ${name}`, () => {
  for (const busy of [false, true]) {
    const { q } = fixture();
    const result = q.admit(text, 'feed', busy, quoted);
    if (busy || ordinary) assert.equal(result, busy ? 'busy' : 'ordinary');
    else assert.deepEqual(result, { draftKey: 'key-1', requestKey: 'key-1', state: 'queued', unconfirmed: false, text, postedMessage: null });
  }
  const { q } = fixture();
  q.admit('earlier', 'feed');
  const result = q.admit(text, 'feed', false, quoted);
  assert.equal(typeof result === 'string' ? result : 'queued', ordinary ? 'busy' : 'queued');
});

for (const status of [200, 409, 400, 404, 413, 422, 429, 0, 408, 401, 500, 503]) {
  for (const uncertain of [false, true]) test(`settle ${status}, unconfirmed=${uncertain}`, () => {
    const { q } = fixture();
    if (uncertain) q.restore('restored', 'body');
    const item = q.admit('body', 'feed'), key = item.requestKey;
    assert.equal(q.dispatch(), item);
    const notice = q.settle(item, status, status === 200 ? posted : undefined);
    const refused = [400, 404, 413, 422, 429].includes(status);
    assert.deepEqual([item.state, item.unconfirmed, item.postedMessage, notice],
      status === 200 ? ['confirmed', false, posted.id, ''] :
      status === 409 ? ['failed', false, null, 'repeat'] :
      ['failed', refused ? uncertain : true, null, refused ? (uncertain ? 'stillUnconfirmed' : 'notSent') : 'unconfirmed']);
    assert.equal(item.text, 'body');
    assert.equal(item.requestKey === key, status !== 409);
  });
}

for (const state of ['queued', 'sending', 'failed', 'confirmed', 'detached']) test(`refusals: ${state}`, () => {
  let changes = 0;
  const { q } = fixture(() => changes++);
  const item = q.admit('body', 'feed');
  if (state !== 'queued' && state !== 'detached') q.dispatch();
  if (state === 'failed' || state === 'confirmed') q.settle(item, state === 'failed' ? 400 : 200);
  if (state === 'detached') q.remove(item, 'feed');
  const before = changes;
  if (state !== 'sending') assert.equal(q.settle(item, 500), undefined);
  if (state !== 'failed') {
    assert.equal(q.retry(item), false);
    assert.equal(q.edit(item, '', false), undefined);
  } else {
    assert.equal(q.edit(item, ' ', false), undefined);
    assert.equal(q.edit(item, '', true), undefined);
  }
  if (state === 'detached') assert.equal(q.remove(item, 'feed'), undefined);
  assert.equal(changes, before);
});

test('ordering: FIFO pauses unknown outcomes and continues after notSent', () => {
  const results = [];
  for (const status of [500, 400]) {
    const { q } = fixture();
    const a = q.admit('a', 'feed'), b = q.admit('b', 'feed');
    const first = q.dispatch();
    q.settle(first, status);
    results.push([first === a, q.dispatch() === (status === 500 ? undefined : b)]);
  }
  assert.deepEqual(results, [[true, true], [true, true]]);
});
for (const uncertain of [false, true]) test(`ordering: Retry keeps place=${uncertain}`, () => {
  const { q } = fixture();
  const a = failed(q, uncertain ? 500 : 400), b = q.admit('b', 'feed');
  const identity = [a.text, a.requestKey];
  assert.equal(q.retry(a), true);
  assert.deepEqual(q.items, uncertain ? [a, b] : [b, a]);
  assert.deepEqual([a.state, a.text, a.requestKey], ['queued', ...identity]);
});
test('ordering: 409 rotates requestKey while preserving draftKey', () => {
  const { q } = fixture();
  const a = q.admit('body', 'feed'), draft = a.draftKey;
  q.dispatch();
  q.settle(a, 409);
  assert.deepEqual([a.draftKey, a.requestKey === draft], [draft, false]);
});
for (const state of ['queued', 'sending', 'failed', 'confirmed']) test(`ordering: capacity counts ${state}`, () => {
  const { q } = fixture();
  for (let i = 0; i < 10; i++) {
    const item = q.admit(`body ${i}`, 'feed');
    if (state !== 'queued') q.dispatch();
    if (state === 'failed' || state === 'confirmed') q.settle(item, state === 'failed' ? 400 : 200, posted);
    // Sending items serialize, so this row includes one sending and nine queued.
  }
  assert.equal(q.admit('eleventh', 'feed', true), 'capacity');
  if (state === 'failed') assert.equal(q.retry(q.items[0]), true);
  q.remove(q.items[0], 'feed');
  assert.equal(q.admit('freed slot', 'feed').state, 'queued');
});

test('dispatch locks survive removal until the detached request settles', () => {
  const { q } = fixture();
  const a = q.admit('a', 'feed');
  assert.equal(q.dispatch(true), undefined);
  assert.equal(q.dispatch(), a);
  q.remove(a, 'feed');
  assert.equal(q.admit(' ', 'feed'), 'busy');
  const b = q.admit('b', 'feed');
  assert.equal(q.dispatch(), undefined);
  assert.equal(q.settle(a, 400), 'notSent');
  assert.equal(q.dispatch(), b);
  q.settle(b, 200);
  assert.equal(q.dispatch(), undefined);
});

for (const [name, view, removed] of [
  ['stream first', { ids: [posted.id] }, true],
  ['unrelated ID', { ids: ['other'] }, false],
  ['other topic', { ids: [], topic: 'topic-b' }, true],
  ['cursor reached', { ids: [], topic: posted.topic, applied: posted.seq }, true],
  ['cursor behind', { ids: [], topic: posted.topic, applied: posted.seq - 1n }, false],
  ['feed cursor', { ids: [], applied: posted.seq }, false],
  ['missing cursor', { ids: [], topic: posted.topic }, false],
]) test(`delivery: ${name}`, () => {
  const { q } = fixture();
  const a = q.admit('body', 'feed');
  q.deliver(view);
  assert.equal(q.items.length, 1); // An unconfirmed delivery cannot identify this send.
  q.dispatch();
  q.settle(a, 200, posted, view);
  assert.equal(q.items.length, removed ? 0 : 1);
});
for (const [name, view, removed] of [
  ['response first', { ids: [posted.id] }, true],
  ['move from topic', { ids: [], topic: posted.topic, moved: { fromTopic: posted.topic, ids: [posted.id] } }, true],
  ['move from elsewhere', { ids: [], topic: posted.topic, moved: { fromTopic: 'other', ids: [posted.id] } }, false],
  ['unrelated move', { ids: [], topic: posted.topic, moved: { fromTopic: posted.topic, ids: ['other'] } }, false],
  ['feed move', { ids: [], moved: { fromTopic: posted.topic, ids: [posted.id] } }, false],
]) test(`delivery after response: ${name}`, () => {
  const { q } = fixture();
  const a = q.admit('body', 'feed');
  q.dispatch(); q.settle(a, 200, posted);
  q.deliver(view);
  assert.equal(q.items.length, removed ? 0 : 1);
});

for (const uncertain of [false, true]) test(`Edit returns raw identity, uncertainty=${uncertain}`, () => {
  const { q } = fixture();
  const a = failed(q, uncertain ? 500 : 409);
  q.restore('stale', 'stale');
  assert.deepEqual(q.edit(a, '', false), { draftKey: a.draftKey, text: a.text, requestKey: a.requestKey, unconfirmed: uncertain });
  assert.equal(q.items.length, 0);
  assert.equal(q.changeText('nonempty edit'), undefined);
  const b = q.admit('nonempty edit', 'feed');
  assert.deepEqual([b.requestKey, b.unconfirmed, q.restoration], [a.requestKey, uncertain, null]);
});
for (const [name, act, reused] of [
  ['unchanged', () => {}, true], ['nonempty edit', q => q.changeText('changed'), false],
  ['empty', q => q.changeText(''), false], ['matching sent', q => q.sent('saved'), false],
  ['unrelated sent', q => q.sent('other'), true],
  ['ordinary 200', q => q.ordinarySettled(200), false], ['ordinary 409', q => q.ordinarySettled(409), false],
  ['ordinary refused', q => q.ordinarySettled(422), true],
]) test(`draft restoration: ${name}`, () => {
  const { q } = fixture(); q.restore('saved', 'body'); act(q);
  const a = q.admit('body', 'feed');
  assert.deepEqual([a.requestKey === 'saved', a.unconfirmed], [reused, reused]);
  assert.equal(q.restoration, null);
});
for (const action of ['empty', 'sent', 'confirm']) test(`Edit restoration releases on ${action}`, () => {
  const { q } = fixture(); const a = failed(q); q.edit(a, '', false);
  const fresh = action === 'empty' ? q.changeText('') : action === 'sent' ? q.sent(a.requestKey) : q.ordinarySettled(200);
  assert.equal(typeof fresh, 'string');
  assert.notEqual(q.admit('replacement', 'feed').requestKey, a.requestKey);
});
for (const [name, act, place, text, reused] of [
  ['same raw/place', () => {}, 'feed', ' raw\r\ntext ', true],
  ['different place', () => {}, 'topic', ' raw\r\ntext ', false],
  ['different raw', () => {}, 'feed', 'raw\ntext', false],
  ['just before expiry', (_, advance) => advance(299999), 'feed', ' raw\r\ntext ', true],
  ['expiry boundary', (_, advance) => advance(300000), 'feed', ' raw\r\ntext ', false],
  ['expiry callback', (q, advance) => { advance(300000); q.expire(); }, 'feed', ' raw\r\ntext ', false],
  ['pagehide', q => q.pagehide(), 'feed', ' raw\r\ntext ', false],
  ['sent', (q, _, a) => q.sent(a.requestKey), 'feed', ' raw\r\ntext ', false],
  ['unrelated sent', q => q.sent('other'), 'feed', ' raw\r\ntext ', true],
  ['restored precedence', q => q.restore('saved', ' raw\r\ntext '), 'feed', ' raw\r\ntext ', false],
]) test(`Remove hold: ${name}`, () => {
  const { q, advance } = fixture(), a = failed(q);
  assert.equal(q.remove(a, 'feed'), a.draftKey);
  act(q, advance, a);
  const b = q.admit(text, place);
  assert.equal(b.requestKey === a.requestKey, reused);
  assert.equal(b.unconfirmed, reused || name === 'restored precedence');
  q.remove(b, place); q.pagehide();
  assert.notEqual(q.admit(text, place).requestKey, a.requestKey);
});
for (const status of [200, 400, 409]) test(`Remove without hold after ${status}`, () => {
  const { q } = fixture(), a = failed(q, status);
  q.remove(a, 'feed');
  assert.equal(q.admit(a.text, 'feed').unconfirmed, false);
});

for (const state of ['queued', 'sending', 'failed', 'confirmed']) {
  for (const unconfirmed of [false, true]) test(`holdsReload: ${state}/${unconfirmed}`, () => {
    assert.equal(holdsReload([{ state, unconfirmed }]), state !== 'confirmed');
    assert.equal(holdsReload([]), false);
  });
}
for (const end of ['remove', 'edit', 'confirm', 'delivery', 'delayed delivery']) test(`synchronous notification: ${end}`, () => {
  const seen = [];
  const { q } = fixture(() => seen.push([holdsReload(q.items), q.items.map(i => i.state)]));
  const a = q.admit('body', 'feed');
  assert.deepEqual(seen, [[true, ['queued']]]);
  q.dispatch();
  if (end === 'remove' || end === 'edit') q.settle(a, 500);
  const before = seen.length;
  if (end === 'remove') q.remove(a, 'feed');
  if (end === 'edit') q.edit(a, '', false);
  if (end === 'confirm') q.settle(a, 200, posted);
  if (end === 'delivery') q.settle(a, 200, posted, { ids: [posted.id] });
  if (end === 'delayed delivery') { q.settle(a, 200, posted); q.deliver({ ids: [posted.id] }); }
  assert.equal(seen.length, before + (end === 'delayed delivery' ? 2 : 1));
  assert.deepEqual(seen.at(-1), [false, end === 'confirm' ? ['confirmed'] : []]);
});

for (const status of [400, 429, 0, 500]) test(`ordinary outcome after Edit: ${status}`, () => {
  const { q } = fixture(), a = failed(q, 400);
  q.edit(a, '', false); q.ordinarySettled(status);
  assert.deepEqual([q.restoration.key, q.restoration.unconfirmed], [a.requestKey, status === 0 || status === 500]);
});
test('notifications expose Retry and preserve a hold while another item remains', () => {
  const seen = [];
  const { q } = fixture(() => seen.push([holdsReload(q.items), q.items.map(i => i.state)]));
  const a = failed(q, 400), b = q.admit('b', 'feed');
  q.retry(a);
  assert.deepEqual(seen.at(-1), [true, ['queued', 'queued']]);
  q.remove(a, 'feed');
  assert.deepEqual(seen.at(-1), [true, ['queued']]);
  q.dispatch(); q.settle(b, 200, posted);
  assert.deepEqual(seen.at(-1), [false, ['confirmed']]);
});
