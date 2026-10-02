// Run with node --test internal/web/testdata/topic_moves.cjs; no browser required.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { runInNewContext } = require('node:vm');
const script = readFileSync('web/static/message-stream-v5.js', 'utf8');

// Model only the DOM and htmx operations used by the production SSE listener.
function page(topic, oldest, sequences) {
  const listeners = {};
  const connection = { dataset: {}, url: '/events?after=0',
    getAttribute() { return this.url; }, setAttribute(_, url) { this.url = url; } };
  const item = (seq) => ({ id: `message-${seq}`, dataset: { eventSeq: String(seq) },
    remove() { items.children.splice(items.children.indexOf(this), 1); } });
  const items = { id: 'message-items', dataset: { topic }, children: sequences.map(item), closest: () => connection };
  const history = { dataset: { oldestSeq: String(oldest) }, href: `?before=${oldest}` };
  const document = { addEventListener: (name, fn) => { listeners[name] = fn; },
    getElementById: (id) => id === 'load-older' ? history : items.children.find((i) => i.id === id) };
  const htmx = { swap(target, data, options, selection) {
    assert.equal(options.settleDelay, 0);
    const incoming = data.nodes.find((i) => '#' + i.id === selection.select);
    const clone = item(incoming.dataset.eventSeq);
    const index = items.children.indexOf(target);
    if (options.swapStyle === 'outerHTML') items.children.splice(index, 1, clone);
    else if (options.swapStyle === 'beforebegin') items.children.splice(index, 0, clone);
    else { assert.equal(options.swapStyle, 'beforeend'); items.children.push(clone); }
  } };
  class DOMParser { parseFromString(data) {
    return { querySelector: (tag) => tag === 'ul' ? { dataset: data.routing } : data.nodes[0], querySelectorAll: () => data.nodes };
  } }
  runInNewContext(script, { document, htmx, DOMParser, URL, location: { href: 'http://localhost/' } });
  return { items, history, move(fromTopic, toTopic, seqs) {
    let prevented = false;
    listeners['htmx:sseBeforeMessage']({ target: items, preventDefault() { prevented = true; },
      detail: { type: 'messages-moved', lastEventId: '99', data: { routing: { fromTopic, toTopic }, nodes: seqs.map(item) } } });
    assert.ok(prevented);
    assert.equal(connection.url, '/events?after=99');
  }, sequences() { return items.children.map((i) => i.dataset.eventSeq); } };
}

test('source removal keeps history bound even when every item leaves', () => {
  const p = page('source', 20, [20, 30]);
  for (let i = 0; i < 2; i++) p.move('source', 'destination', [10, 20, 30]);
  assert.deepEqual(p.sequences(), []);
  assert.equal(p.history.href, '?before=20');
  p.move('destination', 'source', [10, 20, 30]);
  assert.deepEqual(p.sequences(), ['20', '30']);
});
test('destination orders unsorted moves, deduplicates, and leaves older history for paging', () => {
  const p = page('destination', 20, [20, 40]);
  for (let i = 0; i < 2; i++) p.move('source', 'destination', [50, 10, 30, 20]);
  assert.deepEqual(p.sequences(), ['20', '30', '40', '50']);
  assert.equal(p.history.href, '?before=20');
  // Load older replaces the control; a subsequent move uses the extended range.
  p.history.dataset.oldestSeq = '5';
  p.move('source', 'destination', [10]);
  assert.deepEqual(p.sequences(), ['10', '20', '30', '40', '50']);
});
test('empty topics accept moves; sequence comparison retains int64 precision', () => {
  const p = page('destination', 0, []);
  p.move('source', 'destination', ['9007199254740993', '9007199254740992']);
  assert.deepEqual(p.sequences(), ['9007199254740992', '9007199254740993']);
});
test('feeds replace only loaded IDs', () => {
  const p = page(undefined, 20, [20, 40]);
  p.move('source', 'destination', [10, 20, 30]);
  assert.deepEqual(p.sequences(), ['20', '40']);
});
