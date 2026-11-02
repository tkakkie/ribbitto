import test from "node:test";
import assert from "node:assert/strict";
import { unreadState, observe, sequence, unreadSequence, routing, ignored, overflow, retain, loss,
  backoff, retryWait, resumeWait, complete, snapshot, applicable, markApplied, validCount, incrementCount, incrementLabel } from "../src/unread-counts.ts";

const member = "00000000-0000-0000-0000-000000000001";
const other = "00000000-0000-0000-0000-000000000002";
const frame = (extra = {}) => ({ channel: member, topic: other, author: other, seq: 1, ...extra });
for (const [raw, count, text] of [["0", "1", "1"], ["98", "99", "99"], ["99", "100", "99+"],
  ["00100", "101", "99+"], ["9007199254740993", "9007199254740994", "99+"]]) {
  test(`count ${raw}`, () => {
    assert.equal(validCount(raw), true);
    assert.deepEqual(incrementCount(raw), { count, text });
    assert.equal(incrementLabel("Unread 99+ messages", text), `Unread ${text} messages`);
    assert.equal(incrementLabel("No number", text), "No number");
  });
}
for (const raw of [undefined, "", "-1", "1.1", " 1", "1x", "NaN", "{}", "1\n"]) {
  test(`invalid count/snapshot ${String(raw)}`, () => {
    assert.equal(validCount(raw), false);
    assert.throws(() => sequence(raw));
  });
}
for (const raw of ["0", "0001", "9007199254740993123456789"]) {
  test(`snapshot BigInt ${raw}`, () => assert.equal(sequence(raw), BigInt(raw)));
}
for (const [seq, valid] of [[0, true], [Number.MAX_SAFE_INTEGER, true], [-1, false], [1.5, false],
  [Number.MAX_SAFE_INTEGER + 1, false], ["1", false], [undefined, false], [1e100, false]]) {
  test(`frame sequence ${seq}`, () => {
    if (valid) assert.equal(unreadSequence(frame({ seq })), BigInt(seq));
    else assert.throws(() => unreadSequence(frame({ seq })));
  });
}
for (const [name, extra, result] of [["valid", {}, true], ["legacy", { topic: undefined, author: "bad" }, false],
  ["missing author", { author: undefined }, true], ["bad channel", { channel: "bad" }, null],
  ["bad topic", { topic: "" }, null], ["bad author", { author: "bad" }, null],
  ["null topic", { topic: null }, null], ["uppercase UUID", { channel: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA" }, null]]) {
  test(`routing ${name}`, () => {
    if (result === null) assert.throws(() => routing(frame(extra)));
    else assert.equal(routing(frame(extra)), result);
  });
}
for (const [name, author, eligible] of [["other", other, true], ["own", member, false], ["legacy", undefined, false]]) {
  test(`retention ${name}`, () => {
    const initial = unreadState(0n);
    const held = retain(initial, 1n, frame({ author }), member);
    assert.equal(initial.events.size, 0);
    assert.equal(held.events.get(1n).eligible, eligible);
    assert.equal(applicable(held, held.events.get(1n)), eligible);
    assert.equal(applicable(loss(held), held.events.get(1n)), false);
    assert.equal(applicable(held, { ...held.events.get(1n), applied: true }), false);
    assert.equal(ignored(held, 0n), true);
    assert.equal(ignored(held, 1n), true);
    assert.equal(ignored(held, 2n), false);
  });
}
test("retention cap reports highest dropped sequence without mutating inputs", () => {
  let state = unreadState(0n);
  for (let seq = 1000; seq > 0; seq--) state = retain(state, BigInt(seq), frame({ seq }), member);
  assert.equal(overflow({ ...state, events: new Map([...state.events].slice(1)) }), null);
  assert.equal(overflow(state).through, 1000n);
  const recovering = loss(overflow(state).state, overflow(state).through);
  const next = retain(recovering, 1001n, frame({ seq: 1001 }), member);
  assert.equal(next.events.size, 1);
  assert.equal(next.lostThrough, 1000n);
  assert.equal(applicable(next, next.events.get(1001n)), false);
  assert.equal(state.events.size, 1000);
});
test("malformed routing observes its valid sequence before loss; loss is monotonic", () => {
  const state = unreadState(10n);
  const bad = frame({ seq: 20, topic: "bad" });
  const observed = observe(state, unreadSequence(bad));
  assert.throws(() => routing(bad));
  const failed = loss(observed);
  const again = loss(observe(failed, 15n), 5n);
  assert.deepEqual([again.seen, again.lostThrough, again.losses, again.recovering], [20n, 20n, 2, true]);
  assert.equal(state.seen, 10n);
});
for (const [name, successful, received, newLoss, seq, covered] of [
  ["both", true, true, false, 10n, true], ["snapshot first", false, true, false, 10n, null],
  ["response first", true, false, false, 10n, null], ["neither", false, false, false, 10n, null],
  ["new loss", true, true, true, 10n, false], ["older snapshot", true, true, false, 9n, false],
]) {
  test(`recovery ${name}`, () => {
    let state = loss(unreadState(0n), 10n);
    state = { ...state, request: { losses: state.losses, success: successful, received } };
    if (newLoss) state = loss(state, 10n);
    state = snapshot(state, seq);
    const result = complete(state);
    assert.equal(result.covered, covered);
    assert.equal(result.state.recovering, covered !== true);
    assert.equal(result.state.delay, covered === false ? 500 : 250);
    assert.equal(result.state.request === null, covered !== null);
    assert.notEqual(state.request, null);
  });
}
test("no pending request cannot clear recovery", () => assert.equal(complete(loss(unreadState(0n))).covered, null));
for (const value of [null, [], true, "odd", {}]) {
  test(`invalid frame ${JSON.stringify(value)}`, () => assert.throws(() => unreadSequence(value)));
}
for (const [random, wait] of [[0, 0], [0.5, 1000], [1, 2000]]) {
  test(`resume jitter ${random}`, () => assert.equal(resumeWait(random), wait));
}
for (const [delay, want] of [[250, 500], [4000, 8000], [8000, 8000]]) {
  test(`backoff ${delay}`, () => assert.equal(backoff(delay), want));
  for (const random of [0, 0.5, 1]) {
    test(`retry jitter ${delay}/${random}`, () => assert.equal(retryWait(delay, random), delay * (1 + random) / 2));
  }
}
test("snapshots follow delivery order, drop covered events, and reset application once", () => {
  let state = unreadState(0n);
  for (const seq of [1n, 3n, 5n]) state = retain(state, seq, frame({ applied: true }), member);
  state.events.get(5n).applied = true;
  const older = snapshot(snapshot(state, 3n), 2n);
  assert.equal(older.snapshot, 2n);
  assert.deepEqual([...older.events.keys()], [5n]);
  assert.equal(older.events.get(5n).applied, false);
  assert.equal(state.events.get(5n).applied, true);
  assert.equal(applicable(older, older.events.get(5n)), true);
  const applied = markApplied(older, 5n);
  assert.equal(applicable(applied, applied.events.get(5n)), false);
  assert.equal(older.events.get(5n).applied, false);
  assert.equal(ignored(older, 2n), true);
});
