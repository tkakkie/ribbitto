import assert from "node:assert/strict";
import test from "node:test";
import {
  MaxTimerDelay, canReplaceSnapshot, checkSessionExpired, draftKey,
  isSendDetail, matchSentSnapshot, nextEditTime, nextExpiryDelay, nextSendEditTime, normalizeDraftText,
  pendingActionDecision, prepareSendSnapshot, restoredSendKey, selectKeysToTrim, shouldWriteDraft,
} from "../src/drafts.ts";

const memberA = "01000000-0000-4000-8000-000000000001";
const memberB = "01000000-0000-4000-8000-000000000002";
const sessionPage = "019a1b2c-3d4e-7800-8800-000000000001";
const channelA = "02000000-0000-4000-8000-000000000001";
const sendKeyA = "04000000-0000-4000-8000-000000000001";
const sendKeyB = "04000000-0000-4000-8000-000000000002";
const scope = { member: memberA, session: sessionPage };

test("session expiry latches when either clock reaches its bound", () => {
  for (const row of [
    { name: "monotonic clock", deadline: 5_000, perf: 5_000, wall: 10_000 },
    { name: "wall clock", deadline: 50_000, perf: 100, wall: 20_000 },
  ]) {
    const state = { expired: false, deadline: row.deadline, expires: 20_000 };
    assert.deepEqual(checkSessionExpired(state, 4_999, 19_999), { justExpired: false, expired: false }, row.name);
    assert.deepEqual(checkSessionExpired(state, row.perf, row.wall), { justExpired: true, expired: true }, row.name);
    assert.deepEqual(checkSessionExpired(state, 0, 0), { justExpired: false, expired: true }, row.name);
  }
});

test("expiry waits chain within the maximum timer delay", () => {
  for (const row of [
    { name: "chained wait", deadline: MaxTimerDelay + 5_000, perf: 1_000, want: MaxTimerDelay },
    { name: "remaining wait", deadline: 10_000, perf: 2_500, want: 7_500 },
  ]) assert.equal(nextExpiryDelay(row.deadline, row.perf), row.want, row.name);
});

test("blank and over-limit text becomes a tombstone", () => {
  for (const tc of [
    { name: "visible text", input: "hello", want: "hello" },
    { name: "whitespace", input: "   \n\t ", want: "" },
    { name: "4000 code points", input: "😀".repeat(4000), want: "😀".repeat(4000) },
    { name: "over limit", input: "a".repeat(4001), want: "" },
  ]) {
    assert.equal(normalizeDraftText(tc.input), tc.want, tc.name);
  }
});

test("edit and send times advance past earlier writes", () => {
  for (const row of [
    { name: "equal edit time", next: nextEditTime, prev: 100, now: 100, want: 101 },
    { name: "clock rollback", next: nextEditTime, prev: 200, now: 100, want: 201 },
    { name: "later edit", next: nextEditTime, prev: 100, now: 150, want: 150 },
    { name: "later stored send", next: nextSendEditTime, prev: 120, now: 150, want: 151 },
    { name: "later edited send", next: nextSendEditTime, prev: 150, now: 120, want: 150 },
  ]) assert.equal(row.next(row.prev, row.now), row.want, row.name);
});

test("trimming selects oldest own entries with key ordering for ties", () => {
  const ownKeys = Array.from({ length: 51 }, (_, i) => draftKey(memberA, sessionPage, `02000000-0000-4000-8000-${String(i).padStart(12, "0")}`, "feed"));
  const foreignKey = draftKey(memberB, sessionPage, channelA, "feed");
  const times = new Map(ownKeys.map((k, i) => [k, i === 0 || i === 1 ? 10 : 100 + i]));
  for (const row of [
    { name: "before insert excludes foreign and current keys", keys: [foreignKey, ownKeys[1], ownKeys[0], ...ownKeys.slice(2)], limit: 49, except: ownKeys[50] },
    { name: "after write", keys: ownKeys, limit: 50, except: undefined },
  ]) assert.deepEqual(selectKeysToTrim(row.keys, scope, row.limit, (k) => times.get(k), row.except), [ownKeys[0]], row.name);
});

test("send detail requires a key and string body", () => {
  for (const row of [
    { name: "missing key", detail: { key: null, body: "hi", inPlace: true }, want: false },
    { name: "non-string body", detail: { key: sendKeyA, body: 5 }, want: false },
    { name: "missing detail", detail: undefined, want: false },
    { name: "empty body", detail: { key: sendKeyA, body: "" }, want: true },
  ]) assert.equal(isSendDetail(row.detail), row.want, row.name);
});

test("send snapshots keep nonempty text and match only the sent key", () => {
  for (const row of [
    { name: "blank", body: "  ", inPlace: false, want: { text: "", source: sendKeyA, savedKey: null, sending: { key: sendKeyA, inPlace: false } } },
    { name: "nonempty", body: "sent text", inPlace: true, want: { text: "sent text", source: sendKeyA, savedKey: sendKeyA, sending: { key: sendKeyA, inPlace: true } } },
  ]) assert.deepEqual(prepareSendSnapshot({ key: sendKeyA, body: row.body, inPlace: row.inPlace }), row.want, row.name);
  const sentSnap = prepareSendSnapshot({ key: sendKeyA, body: "sent text", inPlace: true });
  for (const row of [
    { name: "matching send", key: sendKeyA, want: { key: sendKeyA, inPlace: true } },
    { name: "other send", key: sendKeyB, want: null },
  ]) assert.deepEqual(matchSentSnapshot(sentSnap.sending, row.key), row.want, row.name);
});

test("snapshot replacement requires an eligible source and unchanged storage", () => {
  for (const tc of [
    { name: "matching source", source: sendKeyA, key: sendKeyA, inPlace: false, last: "v1", cur: "v1", want: true },
    { name: "in-place edited during send", source: null, key: sendKeyA, inPlace: true, last: "v1", cur: "v1", want: true },
    { name: "pending edited during send", source: null, key: sendKeyA, inPlace: false, last: "v1", cur: "v1", want: false },
    { name: "competing tab write", source: sendKeyA, key: sendKeyA, inPlace: true, last: "v1", cur: "v2", want: false },
    { name: "no prior write", source: sendKeyA, key: sendKeyA, inPlace: true, last: undefined, cur: "v1", want: false },
  ]) {
    assert.equal(canReplaceSnapshot(tc.source, tc.key, tc.inPlace, tc.last, tc.cur), tc.want, tc.name);
  }
});

test("pending edits save and removals replace their snapshot", () => {
  for (const row of [
    { name: "edit", detail: { kind: "edit", key: sendKeyA, restoredKey: sendKeyB }, want: { action: "save-edit", savedKey: sendKeyB } },
    { name: "remove", detail: { kind: "remove", key: sendKeyA, restoredKey: null }, want: { action: "replace", sendKey: sendKeyA } },
  ]) assert.deepEqual(pendingActionDecision(row.detail), row.want, row.name);
});

test("restored send keys require nonempty text and a valid UUID", () => {
  for (const row of [
    { name: "unchanged text", text: "hello", key: sendKeyA, want: sendKeyA },
    { name: "empty text", text: "", key: sendKeyA, want: null },
    { name: "invalid key", text: "hello", key: "bad", want: null },
  ]) assert.equal(restoredSendKey(row), row.want, row.name);
});

test("stored timestamp comparisons tolerate malformed values", () => {
  for (const row of [
    { name: "equal", stored: 100, candidate: 100, want: false },
    { name: "later", stored: 105, candidate: 100, want: false },
    { name: "earlier", stored: 99, candidate: 100, want: true },
    { name: "later numeric string", stored: "200", candidate: 100, want: false },
    { name: "equal numeric string", stored: "100", candidate: 100, want: false },
    { name: "earlier numeric string", stored: "50", candidate: 100, want: true },
    { name: "nonnumeric string", stored: "bad", candidate: 100, want: true },
    { name: "NaN", stored: NaN, candidate: 100, want: true },
    { name: "negative", stored: -50, candidate: 100, want: true },
    { name: "equal negative", stored: -50, candidate: -50, want: false },
    { name: "missing", stored: undefined, candidate: 100, want: true },
    { name: "null", stored: null, candidate: 100, want: true },
    { name: "null equals zero", stored: null, candidate: 0, want: false },
  ]) assert.equal(shouldWriteDraft(row.stored, row.candidate), row.want, row.name);
});

test("send times advance past stored timestamps including malformed values", () => {
  for (const row of [
    { name: "numeric string", edited: 120, stored: "200", want: 2001 },
    { name: "nonnumeric string", edited: 120, stored: "bad", want: NaN },
    { name: "empty string", edited: 120, stored: "", want: 120 },
    { name: "NaN", edited: 120, stored: NaN, want: 120 },
    { name: "negative", edited: 120, stored: -50, want: 120 },
    { name: "later negative", edited: -100, stored: -50, want: -49 },
    { name: "missing", edited: 120, stored: undefined, want: 120 },
    { name: "null", edited: 120, stored: null, want: 120 },
  ]) assert.equal(nextSendEditTime(row.edited, row.stored), row.want, row.name);
});

test("trimming orders missing and malformed stored timestamps", () => {
  const ownKeys = Array.from({ length: 5 }, (_, i) => draftKey(memberA, sessionPage, `02000000-0000-4000-8000-${String(i).padStart(12, "0")}`, "feed"));
  const malformedTimes = new Map<string, unknown>([
    [ownKeys[0], undefined],
    [ownKeys[1], NaN],
    [ownKeys[2], -50],
    [ownKeys[3], "200"],
    [ownKeys[4], 100],
  ]);
  for (const row of [
    { name: "missing, NaN, negative and numeric string", keys: [ownKeys[3], ownKeys[4], ownKeys[1], ownKeys[0], ownKeys[2]], read: (k: string) => malformedTimes.get(k), want: [ownKeys[2], ownKeys[0], ownKeys[1], ownKeys[4]] },
    { name: "nonnumeric string", keys: [ownKeys[1], ownKeys[0]], read: (k: string) => k === ownKeys[1] ? "bad" : 100, want: [ownKeys[0]] },
  ]) assert.deepEqual(selectKeysToTrim(row.keys, scope, 1, row.read), row.want, row.name);
});
