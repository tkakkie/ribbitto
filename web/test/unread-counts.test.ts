import test from "node:test";
import assert from "node:assert/strict";
import { unreadState, observe, sequence, retain, loss, retryWait, resumeWait, complete, snapshot,
  applicable, markApplied, incrementCount, incrementLabel, badgeFailure, missingRow,
  snapshotBadgesMatch, mayRequest, failedRequest, receiveFrame,
  type UnreadFrame, type UnreadState } from "../src/unread-counts.ts";

const member = "00000000-0000-0000-0000-000000000001";
const other = "00000000-0000-0000-0000-000000000002";
const frame = (extra: Partial<UnreadFrame> = {}): UnreadFrame =>
  ({ channel: member, topic: other, author: other, seq: 1, ...extra });
function held(state: UnreadState, seq: bigint) {
  const event = state.events.get(seq);
  if (!event) throw new Error(`missing retained fixture ${seq}`);
  return event;
}

test("counts increment exactly and cap only the visible label", () => {
  for (const row of [
    { name: "hidden zero", raw: "0", count: "1", text: "1" },
    { name: "last uncapped", raw: "98", count: "99", text: "99" },
    { name: "cap boundary", raw: "99", count: "100", text: "99+" },
    { name: "leading zeroes", raw: "00100", count: "101", text: "99+" },
    { name: "above safe integer", raw: "9007199254740993", count: "9007199254740994", text: "99+" },
  ]) {
    assert.equal(badgeFailure(row.raw), false, row.name);
    assert.deepEqual(incrementCount(row.raw), { count: row.count, text: row.text }, row.name);
    assert.equal(incrementLabel("Unread 99+ messages", row.text), `Unread ${row.text} messages`, row.name);
    assert.equal(incrementLabel("No number", row.text), "No number", row.name);
  }
});
test("invalid counts fail badge validation and snapshot parsing", () => {
  for (const row of [
    { name: "missing", raw: undefined }, { name: "empty", raw: "" }, { name: "negative", raw: "-1" },
    { name: "fraction", raw: "1.1" }, { name: "leading space", raw: " 1" }, { name: "suffix", raw: "1x" },
    { name: "NaN", raw: "NaN" }, { name: "object text", raw: "{}" }, { name: "newline", raw: "1\n" },
  ]) {
    assert.equal(badgeFailure(row.raw), true, row.name);
    assert.throws(() => sequence(row.raw), Error, row.name);
  }
});
test("snapshot sequences preserve integers beyond Number range", () => {
  for (const row of [
    { name: "zero", raw: "0", want: 0n }, { name: "leading zeroes", raw: "0001", want: 1n },
    { name: "large integer", raw: "9007199254740993123456789", want: 9007199254740993123456789n },
  ]) assert.equal(sequence(row.raw), row.want, row.name);
});
test("received frames validate safe nonnegative sequence numbers", () => {
  for (const row of [
    { name: "zero", seq: 0, action: "ignore", seen: 0n },
    { name: "maximum safe integer", seq: Number.MAX_SAFE_INTEGER, action: "retain", seen: 9007199254740991n },
    { name: "negative", seq: -1, action: "recover", seen: 0n },
    { name: "fraction", seq: 1.5, action: "recover", seen: 0n },
    { name: "unsafe boundary", seq: Number.MAX_SAFE_INTEGER + 1, action: "recover", seen: 0n },
    { name: "string", seq: "1", action: "recover", seen: 0n },
    { name: "missing", seq: undefined, action: "recover", seen: 0n },
    { name: "huge number", seq: 1e100, action: "recover", seen: 0n },
  ]) {
    const result = receiveFrame(unreadState(0n), { ...frame(), seq: row.seq });
    assert.deepEqual([result.action, result.state.seen], [row.action, row.seen], row.name);
  }
});
test("received frames validate routing and ignore legacy payloads", () => {
  for (const row of [
    { name: "valid", extra: {}, action: "retain" },
    { name: "legacy", extra: { topic: undefined, author: "bad" }, action: "ignore" },
    { name: "missing author", extra: { author: undefined }, action: "retain" },
    { name: "bad channel", extra: { channel: "bad" }, action: "recover" },
    { name: "bad topic", extra: { topic: "" }, action: "recover" },
    { name: "bad author", extra: { author: "bad" }, action: "recover" },
    { name: "null topic", extra: { topic: null }, action: "recover" },
    { name: "uppercase UUID", extra: { channel: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA" }, action: "recover" },
  ]) {
    const result = receiveFrame(unreadState(0n), { ...frame(), ...row.extra });
    assert.deepEqual([result.action, result.state.seen, result.state.lostThrough],
      [row.action, 1n, row.action === "recover" ? 1n : 0n], row.name);
  }
});
test("retention preserves eligibility without loss and ignores covered or duplicate frames", () => {
  for (const row of [
    { name: "other", author: other, eligible: true }, { name: "own", author: member, eligible: false },
    { name: "legacy", author: undefined, eligible: false },
  ]) {
    const initial = unreadState(0n);
    const retained = retain(initial, 1n, frame({ author: row.author }), member);
    const event = held(retained, 1n);
    assert.equal(initial.events.size, 0, row.name);
    assert.deepEqual([retained.losses, retained.lostThrough, retained.recovering], [0, 0n, false], row.name);
    assert.equal(event.eligible, row.eligible, row.name);
    assert.equal(applicable(retained, event), row.eligible, row.name);
    assert.equal(applicable(loss(retained), event), false, row.name);
    assert.equal(applicable(retained, { ...event, applied: true }), false, row.name);
    for (const received of [
      { name: "snapshot covered", seq: 0, action: "ignore" }, { name: "duplicate", seq: 1, action: "ignore" },
      { name: "new", seq: 2, action: "retain" },
    ]) assert.equal(receiveFrame(retained, frame({ seq: received.seq })).action, received.action, `${row.name}: ${received.name}`);
  }
});
test("retention cap reports highest dropped sequence without mutating inputs", () => {
  let state = unreadState(0n);
  for (let seq = 1000; seq > 0; seq--) state = retain(state, BigInt(seq), frame({ seq }), member);
  const belowCap = receiveFrame({ ...state, events: new Map([...state.events].slice(1)) }, frame({ seq: 1001 }));
  assert.equal(belowCap.overflowed, false);
  assert.equal(belowCap.state.events.size, 999);
  const duplicate = receiveFrame(state, frame({ seq: 1000 }));
  assert.equal(duplicate.action, "ignore");
  assert.equal(duplicate.state.events.size, 1000);
  const received = receiveFrame(state, frame({ seq: 1001 }));
  assert.equal(received.action, "retain");
  assert.equal(received.overflowed, true);
  const next = retain(received.state, received.seq, frame({ seq: 1001 }), member);
  assert.equal(next.events.size, 1);
  assert.equal(next.lostThrough, 1000n);
  assert.equal(applicable(next, held(next, 1001n)), false);
  assert.equal(state.events.size, 1000);
});
test("received frames distinguish new, old, legacy and malformed events", () => {
  for (const row of [
    { name: "new", extra: {}, snapshotSeq: 0n, action: "retain", seen: 1n, lost: 0n },
    { name: "old", extra: {}, snapshotSeq: 1n, action: "ignore", seen: 1n, lost: 0n },
    { name: "legacy", extra: { topic: undefined, author: "bad" }, snapshotSeq: 0n, action: "ignore", seen: 1n, lost: 0n },
    { name: "invalid sequence", extra: { seq: -1 }, snapshotSeq: 0n, action: "recover", seen: 0n, lost: 0n },
    { name: "invalid routing", extra: { seq: 20, topic: "bad" }, snapshotSeq: 10n, action: "recover", seen: 20n, lost: 20n },
    { name: "invalid old frame", extra: { topic: "bad" }, snapshotSeq: 10n, action: "recover", seen: 10n, lost: 10n },
  ]) {
    const result = receiveFrame(unreadState(row.snapshotSeq), { ...frame(), ...row.extra });
    assert.deepEqual([result.action, result.state.seen, result.state.lostThrough], [row.action, row.seen, row.lost], row.name);
    assert.equal(result.state.recovering, row.action === "recover", row.name);
    assert.equal(result.state.losses, row.action === "recover" ? 1 : 0, row.name);
  }
});
test("malformed routing observes its valid sequence before loss; loss is monotonic", () => {
  const state = unreadState(10n);
  const failed = receiveFrame(state, frame({ seq: 20, topic: "bad" })).state;
  assert.equal(failed.seen, 20n);
  const again = loss(observe(failed, 15n), 5n);
  assert.deepEqual([again.seen, again.lostThrough, again.losses, again.recovering], [20n, 20n, 2, true]);
  assert.equal(state.seen, 10n);
});
test("recovery needs both response and covering snapshot with no new losses", () => {
  for (const row of [
    { name: "both", success: true, received: true, newLosses: 0, seq: 10n, covered: true },
    { name: "snapshot first", success: false, received: true, newLosses: 0, seq: 10n, covered: null },
    { name: "response first", success: true, received: false, newLosses: 0, seq: 10n, covered: null },
    { name: "neither", success: false, received: false, newLosses: 0, seq: 10n, covered: null },
    { name: "new loss", success: true, received: true, newLosses: 1, seq: 10n, covered: false },
    { name: "repeated losses", success: true, received: true, newLosses: 3, seq: 10n, covered: false },
    { name: "older snapshot", success: true, received: true, newLosses: 0, seq: 9n, covered: false },
  ]) {
    let state = loss(unreadState(0n), 10n);
    state = { ...state, request: { losses: state.losses, success: row.success, received: row.received } };
    for (let i = 0; i < row.newLosses; i++) state = loss(state, 10n);
    state = snapshot(state, row.seq);
    const result = complete(state);
    assert.equal(result.covered, row.covered, row.name);
    assert.equal(result.state.recovering, row.covered !== true, row.name);
    assert.equal(result.state.delay, row.covered === false ? 500 : 250, row.name);
    assert.equal(result.state.request === null, row.covered !== null, row.name);
    assert.notEqual(state.request, null, row.name);
  }
  assert.equal(complete(loss(unreadState(0n))).covered, null, "no pending request");
});
test("malformed untrusted frame values trigger recovery", () => {
  for (const row of [
    { name: "null", value: null }, { name: "array", value: [] }, { name: "boolean", value: true },
    { name: "string", value: "odd" }, { name: "object", value: {} },
  ]) {
    const result = receiveFrame(unreadState(0n), row.value);
    assert.deepEqual([result.action, result.state.recovering, result.state.losses], ["recover", true, 1], row.name);
  }
});
test("resume jitter spans zero to two seconds", () => {
  for (const row of [
    { name: "minimum", random: 0, wait: 0 }, { name: "midpoint", random: 0.5, wait: 1000 },
    { name: "maximum", random: 1, wait: 2000 },
  ]) assert.equal(resumeWait(row.random), row.wait, row.name);
});
test("retry backoff doubles up to eight seconds with half-to-full jitter", () => {
  for (const row of [
    { name: "initial", delay: 250, next: 500, minimum: 125, midpoint: 187.5, maximum: 250 },
    { name: "reaching cap", delay: 4000, next: 8000, minimum: 2000, midpoint: 3000, maximum: 4000 },
    { name: "at cap", delay: 8000, next: 8000, minimum: 4000, midpoint: 6000, maximum: 8000 },
  ]) {
    assert.equal(failedRequest({ ...unreadState(0n), delay: row.delay }).delay, row.next, row.name);
    for (const jitter of [
      { name: "minimum", random: 0, wait: row.minimum }, { name: "midpoint", random: 0.5, wait: row.midpoint },
      { name: "maximum", random: 1, wait: row.maximum },
    ]) assert.equal(retryWait(row.delay, jitter.random), jitter.wait, `${row.name}: ${jitter.name}`);
  }
});
test("snapshots follow delivery order, drop covered events, and reset application once", () => {
  let state = unreadState(0n);
  for (const seq of [1n, 3n, 5n]) state = retain(state, seq, frame(), member);
  held(state, 5n).applied = true;
  const older = snapshot(snapshot(state, 3n), 2n);
  assert.equal(older.snapshot, 2n);
  assert.deepEqual([...older.events.keys()], [5n]);
  assert.equal(held(older, 5n).applied, false);
  assert.equal(held(state, 5n).applied, true);
  assert.equal(applicable(older, held(older, 5n)), true);
  const applied = markApplied(older, 5n);
  assert.equal(applicable(applied, held(applied, 5n)), false);
  assert.equal(held(older, 5n).applied, false);
  assert.equal(receiveFrame(older, frame({ seq: 2 })).action, "ignore");
});
test("badge validation requires count, numeric text and accessible label", () => {
  for (const row of [
    { name: "complete", raw: "0", numeric: true, label: true, failure: false },
    { name: "missing count", raw: undefined, numeric: true, label: true, failure: true },
    { name: "bad count", raw: "bad", numeric: true, label: true, failure: true },
    { name: "missing numeric", raw: "0", numeric: false, label: true, failure: true },
    { name: "missing label", raw: "0", numeric: true, label: false, failure: true },
    { name: "both missing", raw: "0", numeric: false, label: false, failure: true },
  ]) assert.equal(badgeFailure(row.raw, row.numeric, row.label), row.failure, row.name);
});
test("missing rows are skipped while present rows require validation", () => {
  for (const row of [{ name: "absent", value: null, skip: true }, { name: "present", value: {}, skip: false }])
    assert.equal(missingRow(row.value), row.skip, row.name);
});
test("snapshot badges match only with identical lengths and raw counts", () => {
  for (const row of [
    { name: "empty", expectedLength: 0, actualLength: 0, expected: undefined, actual: undefined, matches: true },
    { name: "missing badge", expectedLength: 1, actualLength: 0, expected: undefined, actual: undefined, matches: false },
    { name: "extra badge", expectedLength: 0, actualLength: 1, expected: undefined, actual: undefined, matches: false },
    { name: "equal", expectedLength: 1, actualLength: 1, expected: "0", actual: "0", matches: true },
    { name: "different raw spelling", expectedLength: 1, actualLength: 1, expected: "01", actual: "1", matches: false },
    { name: "missing count", expectedLength: 1, actualLength: 1, expected: "0", actual: undefined, matches: false },
  ]) assert.equal(snapshotBadgesMatch(row.expectedLength, row.actualLength, row.expected, row.actual), row.matches, row.name);
});
test("requests need a connected stream with neither pending request nor timer", () => {
  for (const row of [
    { name: "idle", pending: false, timer: false, connected: true, allowed: true },
    { name: "request", pending: true, timer: false, connected: true, allowed: false },
    { name: "timer", pending: false, timer: true, connected: true, allowed: false },
    { name: "both", pending: true, timer: true, connected: true, allowed: false },
    { name: "disconnected", pending: false, timer: false, connected: false, allowed: false },
  ]) assert.equal(mayRequest({ ...unreadState(0n), request: row.pending ?
    { losses: 0, received: false, success: false } : null }, row.timer, row.connected), row.allowed, row.name);
});
test("429 request failure retries at backoff without completing recovery", () => {
  for (const row of [{ name: "initial", delay: 250, next: 500 }, { name: "capped", delay: 8000, next: 8000 }]) {
    const state = { ...unreadState(10n), delay: row.delay, request: { losses: 0, received: true, success: false } };
    const failed = failedRequest(state);
    assert.deepEqual([failed.request, failed.recovering, failed.losses, failed.delay], [null, true, 1, row.next], row.name);
    assert.equal(mayRequest(failed, false, true), true, row.name);
    assert.equal(complete(failed).covered, null, row.name);
  }
});
