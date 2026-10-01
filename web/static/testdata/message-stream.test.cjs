// Run with: node --test web/static/testdata/message-stream.test.cjs
// Exercise event ordering without a browser; real htmx/layout need manual QA.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { runInNewContext } = require("node:vm");
const source = readFileSync(`${__dirname}/../message-stream-v3.js`, "utf8");

function page(atBottom = false) {
  const handlers = new Map();
  const nodes = new Map();
  const scrolled = [];
  const pane = { scrollHeight: 1000, scrollTop: atBottom ? 800 : 100, clientHeight: 200 };
  const status = { childNodes: [], append(node) { this.childNodes.push(node); } };
  const connection = {
    dataset: {}, url: "/events?after=0",
    getAttribute() { return this.url; },
    setAttribute(_, value) { this.url = value; },
  };
  const items = { id: "message-items", closest: () => connection };
  nodes.set("message-items", items);
  nodes.set("message-list", pane);
  nodes.set("message-status", status);
  const emit = (name, event) => handlers.get(name)?.(event);
  runInNewContext(source, {
    document: {
      addEventListener: (name, handler) => handlers.set(name, handler),
      getElementById: (id) => nodes.get(id),
      createTextNode: (text) => text,
    },
    DOMParser: class { parseFromString(item) { return { querySelector: () => item }; } },
    htmx: {
      swap(target, item, options, callbacks) {
        assert.equal(options.swapStyle, nodes.has(item.id) ? "outerHTML" : "beforeend");
        if (!nodes.has(item.id)) items.lastElementChild = item;
        nodes.set(item.id, item);
        callbacks.afterSettleCallback();
      },
    },
    URL, location: { href: "http://localhost/channel" },
  });
  return {
    pane, status, scrolled, connection,
    response(id) {
      const form = { id: "message-composer", dataset: id ? { postedMessage: id } : {} };
      emit("htmx:afterSettle", { target: form });
      // Repeated notifications must not re-arm an already consumed marker.
      emit("htmx:afterSettle", { target: form });
      assert.equal(form.dataset.postedMessage, undefined);
    },
    delivery(id) {
      const item = { id, dataset: { announcement: id }, scrollIntoView: () => scrolled.push(id) };
      let cancelled = false;
      emit("htmx:sseBeforeMessage", {
        target: items, detail: { data: item, lastEventId: "42" },
        preventDefault() { cancelled = true; },
      });
      assert.ok(cancelled);
    },
    reconnect() {
      const listeners = [];
      emit("htmx:sseOpen", { detail: { source: { addEventListener: (name) => listeners.push(name) } } });
      assert.deepEqual(listeners, ["reset"]);
    },
  };
}

for (const atBottom of [false, true]) {
  for (const order of ["stream first", "response first", "reconnect"]) {
    test(`${order}, at bottom=${atBottom}`, () => {
      const p = page(atBottom);
      if (order === "stream first") p.delivery("own");
      p.response("own");
      if (order !== "stream first") {
        assert.deepEqual(p.scrolled, []);
        if (order === "reconnect") p.reconnect();
        p.delivery("other");
        assert.deepEqual(p.scrolled, []);
        p.delivery("own");
      }
      assert.deepEqual(p.scrolled, ["own"]);
      assert.equal(p.pane.scrollTop, p.pane.scrollHeight);
      // Replay replaces the message without another scroll or announcement.
      p.pane.scrollTop = 100;
      const announcements = p.status.childNodes.length;
      p.delivery("own");
      assert.deepEqual(p.scrolled, ["own"]);
      assert.equal(p.pane.scrollTop, 100);
      assert.equal(p.status.childNodes.length, announcements);
      assert.equal(p.connection.url, "/events?after=42");
    });
  }
  test(`failed post then another member, at bottom=${atBottom}`, () => {
    const p = page(atBottom);
    p.response();
    p.delivery("other");
    assert.deepEqual(p.scrolled, []);
    assert.equal(p.pane.scrollTop, atBottom ? p.pane.scrollHeight : 100);
    assert.deepEqual(p.status.childNodes, ["other\n"]);
  });
}

test("pending successes survive later composer replacements and reconnect", () => {
  const p = page();
  p.response("first");
  p.response(); // A failed attempt must not discard a previous success.
  p.response("second");
  p.reconnect();
  p.delivery("other");
  assert.deepEqual(p.scrolled, []);
  p.delivery("first");
  p.delivery("second");
  assert.deepEqual(p.scrolled, ["first", "second"]);
});

test("early own message followed by a newer message still targets the own item", () => {
  const p = page();
  p.delivery("own");
  p.delivery("other");
  p.response("own");
  assert.deepEqual(p.scrolled, ["own"]);
});
