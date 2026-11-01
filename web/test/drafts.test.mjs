import assert from "node:assert/strict";
import test from "node:test";
import {
  DraftPrefix, MaxTimerDelay, SignedOutNoticeKey, canReplaceSnapshot, checkSessionExpired,
  collectDraftPlaces, decideCleanupKey, draftKey, isOwnDraftKey, isSessionDraftKey, isUUID, isUUIDv7,
  isSendDetail, isValidDraftEntry, marked, matchSentSnapshot, nextEditTime, nextExpiryDelay, nextSendEditTime,
  normalizeDraftText, parseDraftEntry, parseDraftKey, parsePageScope, parseSidebarPlace,
  pendingActionDecision, prepareSendSnapshot, restoredSendKey, selectKeysToTrim, shouldWriteDraft,
  sweepKeys, uuidMillisecondPrefix,
} from "../src/drafts.ts";

const memberA = "01000000-0000-4000-8000-000000000001";
const memberB = "01000000-0000-4000-8000-000000000002";
const sessionOlder = "019a1b2c-3d4d-7800-8800-000000000000";
const sessionPage = "019a1b2c-3d4e-7800-8800-000000000001";
const sessionSameMs = "019a1b2c-3d4e-7999-8800-000000000002";
const sessionNewer = "019a1b2c-3d4f-7800-8800-000000000003";
const channelA = "02000000-0000-4000-8000-000000000001";
const topicA = "03000000-0000-4000-8000-000000000001";
const sendKeyA = "04000000-0000-4000-8000-000000000001";
const sendKeyB = "04000000-0000-4000-8000-000000000002";
const dayMs = 24 * 60 * 60 * 1000;
const nowMs = 1_700_000_000_000;
const scope = { member: memberA, session: sessionPage, expires: nowMs + dayMs, rendered: nowMs - 1_000, deadline: dayMs + 1_000 };

test("draft key grammar and ownership", () => {
  for (const tc of [
    { name: "valid feed key", key: draftKey(memberA, sessionPage, channelA), want: { member: memberA, session: sessionPage, channel: channelA, topic: "feed" }, own: true },
    { name: "valid topic key", key: draftKey(memberA, sessionPage, channelA, topicA), want: { member: memberA, session: sessionPage, channel: channelA, topic: topicA }, own: true },
    { name: "foreign member", key: draftKey(memberB, sessionPage, channelA, "feed"), want: { member: memberB, session: sessionPage, channel: channelA, topic: "feed" }, own: false },
    { name: "foreign session", key: draftKey(memberA, sessionNewer, channelA, "feed"), want: { member: memberA, session: sessionNewer, channel: channelA, topic: "feed" }, own: false },
    { name: "wrong prefix", key: `other:${memberA}:${sessionPage}:${channelA}:feed`, want: null, own: false },
    { name: "uppercase uuid", key: draftKey(memberA, sessionPage.toUpperCase(), channelA), want: null, own: false },
    { name: "v4 session instead of v7", key: draftKey(memberA, memberA, channelA), want: null, own: false },
    { name: "invalid v7 variant", key: draftKey(memberA, "019a1b2c-3d4e-7800-c800-000000000001", channelA), want: null, own: false },
    { name: "invalid place", key: `${DraftPrefix}${memberA}:${sessionPage}:${channelA}:not-a-uuid`, want: null, own: false },
    { name: "extra segment", key: `${draftKey(memberA, sessionPage, channelA)}:extra`, want: null, own: false },
  ]) {
    const parsed = parseDraftKey(tc.key);
    assert.deepEqual(parsed, tc.want, tc.name);
    assert.equal(isOwnDraftKey(parsed, scope), tc.own, tc.name);
  }
  assert.equal(isUUID(sendKeyA), true);
  assert.equal(isUUID("not-a-uuid"), false);
  assert.equal(isUUIDv7(sessionPage), true);
  assert.equal(isUUIDv7(memberA), false);
  assert.equal(uuidMillisecondPrefix(sessionPage), "019a1b2c3d4e");
  assert.equal(isSessionDraftKey(draftKey(memberB, sessionPage, channelA), sessionPage), true);
  assert.equal(isSessionDraftKey(draftKey(memberA, sessionOlder, channelA), sessionPage), false);
});

const makeStorage = (entries, onGet) => {
  const reads = [];
  const removed = [];
  const storage = Object.create({
    get reads() { return reads; },
    get removed() { return removed; },
    getItem(k) {
      reads.push(k);
      if (onGet) onGet(k);
      return Object.hasOwn(storage, k) ? storage[k] : null;
    },
    setItem(k, v) {
      storage[k] = String(v);
    },
    removeItem(k) {
      removed.push(k);
      delete storage[k];
    },
  });
  for (const [k, v] of entries) storage[k] = v;
  return storage;
};

test("cleanup decisions across sign-in and organisation horizons without reading foreign values", () => {
  const ownValidKey = draftKey(memberA, sessionPage, channelA, "feed");
  const ownInvalidKey = draftKey(memberA, sessionPage, channelA, topicA);
  const sameSessionForeignMember = draftKey(memberB, sessionPage, channelA, "feed");
  const olderForeign = draftKey(memberB, sessionOlder, channelA, "feed");
  const sameMsForeign = draftKey(memberB, sessionSameMs, channelA, "feed");
  const newerForeign = draftKey(memberA, sessionNewer, channelA, "feed");
  const malformedKey = `${DraftPrefix}malformed`;

  for (const tc of [
    { key: SignedOutNoticeKey, horizon: sessionPage, member: memberA, want: "ignore" },
    { key: malformedKey, horizon: sessionPage, member: memberA, want: "remove" },
    { key: sameSessionForeignMember, horizon: sessionPage, member: memberA, want: "remove" },
    { key: ownValidKey, horizon: sessionPage, member: memberA, want: "validate" },
    { key: olderForeign, horizon: sessionPage, member: memberA, want: "remove" },
    { key: sameMsForeign, horizon: sessionPage, member: memberA, want: "keep" },
    { key: newerForeign, horizon: sessionPage, member: memberA, want: "keep" },
    { key: ownValidKey, horizon: sessionSameMs, member: undefined, want: "keep" },
    { key: olderForeign, horizon: sessionSameMs, member: undefined, want: "remove" },
  ]) {
    assert.equal(decideCleanupKey(tc.key, tc.horizon, tc.member), tc.want, tc.key);
  }

  const validValue = JSON.stringify({ member: memberA, session: sessionPage, expires: scope.expires, text: "hi", at: nowMs, key: null });
  const seed = [
    [SignedOutNoticeKey, sessionPage],
    [malformedKey, validValue],
    [sameSessionForeignMember, validValue],
    [ownValidKey, validValue],
    [ownInvalidKey, "{broken"],
    [olderForeign, validValue],
    [sameMsForeign, validValue],
    [newerForeign, validValue],
  ];
  const orgStorage = makeStorage(seed);
  sweepKeys(orgStorage, sessionPage, scope, nowMs);
  assert.deepEqual(orgStorage.removed, [malformedKey, sameSessionForeignMember, ownInvalidKey, olderForeign]);
  assert.deepEqual(orgStorage.reads, [ownValidKey, ownInvalidKey]);
  assert.deepEqual(Object.keys(orgStorage), [SignedOutNoticeKey, ownValidKey, sameMsForeign, newerForeign]);

  const signInStorage = makeStorage(seed);
  sweepKeys(signInStorage, sessionPage, null, nowMs);
  assert.deepEqual(signInStorage.removed, [malformedKey, olderForeign]);
  assert.deepEqual(signInStorage.reads, []);

  const placeReads = [];
  const values = new Map(seed);
  assert.deepEqual([...collectDraftPlaces([ownValidKey, sameMsForeign, newerForeign], scope, nowMs, (k) => { placeReads.push(k); return values.get(k) ?? null; })], [channelA, `${channelA}:feed`]);
  assert.deepEqual(placeReads, [ownValidKey]);
});

test("cleanup deletes earlier older-session key before a later own-session getItem throws", () => {
  const olderForeign = draftKey(memberB, sessionOlder, channelA, "feed");
  const ownKey = draftKey(memberA, sessionPage, channelA, "feed");
  const laterMalformed = `${DraftPrefix}malformed`;
  const validValue = JSON.stringify({ member: memberA, session: sessionPage, expires: scope.expires, text: "hi", at: nowMs, key: null });
  const storage = makeStorage(
    [
      [olderForeign, validValue],
      [ownKey, validValue],
      [laterMalformed, validValue],
    ],
    (k) => {
      if (k === ownKey) throw new Error("getItem failed");
    },
  );
  assert.throws(() => sweepKeys(storage, sessionPage, scope, nowMs), /getItem failed/);
  assert.equal(Object.hasOwn(storage, olderForeign), false);
  assert.deepEqual(storage.removed, [olderForeign]);
  assert.equal(Object.hasOwn(storage, laterMalformed), true);
});

let adapterRun = 0;
async function withAdapter(storage, nowFn, fn) {
  const prevWindow = globalThis.window;
  const prevDocument = globalThis.document;
  const prevSetTimeout = globalThis.setTimeout;
  const prevClearTimeout = globalThis.clearTimeout;
  const prevDateNow = Date.now;
  const streamAttrs = new Map([
    ["data-member", memberA],
    ["data-session", sessionPage],
    ["data-session-expires", String(scope.expires)],
    ["data-rendered-at", String(scope.rendered)],
  ]);
  const formAttrs = new Map([["data-draft-channel", channelA]]);
  const stream = { getAttribute: (name) => streamAttrs.get(name) ?? null };
  const form = { getAttribute: (name) => formAttrs.get(name) ?? null, hasAttribute: (name) => name === "hx-post" };
  const body = Object.assign(new EventTarget(), { value: "" });
  globalThis.window = Object.assign(new EventTarget(), { localStorage: storage });
  globalThis.document = Object.assign(new EventTarget(), {
    hidden: false,
    querySelector(sel) {
      if (sel === "#organization-stream") return stream;
      if (sel === "#message-composer") return form;
      if (sel === "#message-body") return body;
      return null;
    },
  });
  globalThis.setTimeout = () => 0;
  globalThis.clearTimeout = () => {};
  Date.now = nowFn;
  try {
    await import(new URL(`../src/message-drafts.ts?case=${++adapterRun}`, import.meta.url).href);
    if (fn) await fn({ body });
  } finally {
    Date.now = prevDateNow;
    globalThis.setTimeout = prevSetTimeout;
    globalThis.clearTimeout = prevClearTimeout;
    globalThis.document = prevDocument;
    globalThis.window = prevWindow;
  }
}

test("cleanup samples clock per own-entry validation at the seven-day boundary and keeps v7 exception boundary", async () => {
  const brokenKey = draftKey(memberA, sessionPage, "02000000-0000-4000-8000-000000000000", "feed");
  const firstKey = draftKey(memberA, sessionPage, channelA, "feed");
  const secondKey = draftKey(memberA, sessionPage, channelA, topicA);
  const boundaryValue = JSON.stringify({
    member: memberA,
    session: sessionPage,
    expires: scope.expires,
    text: "boundary",
    at: nowMs - 7 * dayMs + 1,
    key: null,
  });

  let adapterCalls = 0;
  const adapterStorage = makeStorage([
    [firstKey, boundaryValue],
    [secondKey, boundaryValue],
  ]);
  await withAdapter(adapterStorage, () => nowMs + (adapterCalls++ > 0 ? 1 : 0));
  assert.deepEqual(adapterStorage.removed, [secondKey]);
  assert.deepEqual(Object.keys(adapterStorage), [firstKey]);

  let directCalls = 0;
  const directStorage = makeStorage([
    [brokenKey, "{broken"],
    [firstKey, boundaryValue],
    [secondKey, boundaryValue],
  ]);
  sweepKeys(directStorage, sessionPage, scope, () => nowMs + directCalls++);
  assert.equal(directCalls, 2);
  assert.deepEqual(directStorage.removed, [brokenKey, secondKey]);
  assert.deepEqual(Object.keys(directStorage), [firstKey]);

  const throwingClockStorage = makeStorage([[firstKey, boundaryValue], [secondKey, boundaryValue]]);
  let throwOnce = true;
  sweepKeys(throwingClockStorage, sessionPage, scope, () => {
    if (throwOnce) {
      throwOnce = false;
      throw new Error("clock failed");
    }
    return nowMs;
  });
  assert.deepEqual(throwingClockStorage.removed, [firstKey]);
  assert.deepEqual(Object.keys(throwingClockStorage), [secondKey]);
});

test("markers and collectDraftPlaces sample clock per own-entry validation and keep v7 exception boundary", async () => {
  const channelB = "02000000-0000-4000-8000-000000000002";
  const brokenKey = draftKey(memberA, sessionPage, "02000000-0000-4000-8000-000000000000", "feed");
  const firstKey = draftKey(memberA, sessionPage, channelB, "feed");
  const secondKey = draftKey(memberA, sessionPage, channelB, topicA);
  const foreignKey = draftKey(memberB, sessionSameMs, channelB, "feed");
  const boundaryValue = JSON.stringify({
    member: memberA,
    session: sessionPage,
    expires: scope.expires,
    text: "boundary",
    at: nowMs - 7 * dayMs + 1,
    key: null,
  });

  const makeMarker = (href) => ({
    hidden: true,
    closest(sel) {
      return sel === "a" ? { href } : null;
    },
  });
  const feedMarker = makeMarker(`https://example.test/organizations/acme/channels/${channelB}`);
  const topicMarker = makeMarker(`https://example.test/organizations/acme/channels/${channelB}/topics/${topicA}`);
  const sidebar = {
    id: "sidebar-channels",
    querySelectorAll() {
      return [feedMarker, topicMarker];
    },
  };

  let markerCalls = 0;
  let shouldThrow = false;
  const adapterStorage = makeStorage([
    [firstKey, boundaryValue],
    [secondKey, boundaryValue],
  ]);
  await withAdapter(
    adapterStorage,
    () => {
      if (shouldThrow && markerCalls++ >= 2) throw new Error("clock failed");
      if (markerCalls > 0) return nowMs + (markerCalls++ >= 3 ? 1 : 0);
      return nowMs;
    },
    () => {
      markerCalls = 1;
      globalThis.document.dispatchEvent(new CustomEvent("htmx:load", { detail: { elt: sidebar } }));
      assert.deepEqual([feedMarker.hidden, topicMarker.hidden], [false, true]);

      markerCalls = 0;
      shouldThrow = true;
      globalThis.document.dispatchEvent(new CustomEvent("htmx:load", { detail: { elt: sidebar } }));
      assert.deepEqual([feedMarker.hidden, topicMarker.hidden], [true, true]);
    },
  );

  let directCalls = 0;
  const directValues = new Map([
    [foreignKey, boundaryValue],
    [brokenKey, "{broken"],
    [firstKey, boundaryValue],
    [secondKey, boundaryValue],
  ]);
  const directPlaces = collectDraftPlaces(
    [foreignKey, brokenKey, firstKey, secondKey],
    scope,
    () => nowMs + (directCalls++ >= 2 ? 1 : 0),
    (k) => directValues.get(k) ?? null,
  );
  assert.equal(directCalls, 3);
  assert.deepEqual([...directPlaces], [channelB, `${channelB}:feed`]);
});

test("page scope and entry validity bounds", () => {
  assert.deepEqual(parsePageScope({ member: memberA, session: sessionPage, expires: String(scope.expires), rendered: String(scope.rendered) }), scope);
  assert.deepEqual(parsePageScope({ member: memberA, session: sessionPage, expires: "", rendered: "" }), {
    member: memberA, session: sessionPage, expires: 0, rendered: 0, deadline: 0,
  });
  assert.deepEqual(parsePageScope({ member: memberA, session: sessionPage, expires: "100", rendered: "" }), {
    member: memberA, session: sessionPage, expires: 100, rendered: 0, deadline: 100,
  });
  assert.deepEqual(parsePageScope({ member: memberA, session: sessionPage, expires: "", rendered: "50" }), {
    member: memberA, session: sessionPage, expires: 0, rendered: 50, deadline: -50,
  });
  for (const bad of [
    { member: memberA, session: memberA, expires: "100", rendered: "50" },
    { member: memberA, session: sessionPage, expires: null, rendered: "50" },
    { member: memberA, session: sessionPage, expires: "100", rendered: null },
    { member: memberA, session: sessionPage, expires: undefined, rendered: "50" },
    { member: memberA, session: sessionPage, expires: "100", rendered: undefined },
    { member: memberA, session: sessionPage, expires: "1.5", rendered: "50" },
  ]) {
    assert.equal(parsePageScope(bad), null);
  }

  const base = { member: memberA, session: sessionPage, expires: scope.expires, text: "😀".repeat(4000), at: nowMs, key: sendKeyA };
  for (const tc of [
    { name: "valid 4000 astral code points", entry: base, want: true },
    { name: "age just under 7 days", entry: { ...base, at: nowMs - 7 * dayMs + 1 }, want: true },
    { name: "clock skew exactly 1 day ahead", entry: { ...base, at: nowMs + dayMs }, want: true },
    { name: "wrong member", entry: { ...base, member: memberB }, want: false },
    { name: "wrong session", entry: { ...base, session: sessionNewer }, want: false },
    { name: "wrong expiry", entry: { ...base, expires: scope.expires + 1 }, want: false },
    { name: "over 4000 code points", entry: { ...base, text: "a".repeat(4001) }, want: false },
    { name: "non-string text", entry: { ...base, text: 123 }, want: false },
    { name: "7 days old", entry: { ...base, at: nowMs - 7 * dayMs }, want: false },
    { name: "more than 1 day ahead", entry: { ...base, at: nowMs + dayMs + 1 }, want: false },
    { name: "non-number at", entry: { ...base, at: "100" }, want: false },
    { name: "null entry", entry: null, want: false },
  ]) {
    assert.equal(isValidDraftEntry(tc.entry, scope, nowMs), tc.want, tc.name);
  }
  assert.equal(parseDraftEntry("{invalid"), null);
});

test("session expiry on both clocks, latching and chained timer delay", () => {
  const byPerf = { expired: false, deadline: 5_000, expires: 20_000 };
  assert.deepEqual(checkSessionExpired(byPerf, 4_999, 19_999), { justExpired: false, expired: false });
  assert.deepEqual(checkSessionExpired(byPerf, 5_000, 10_000), { justExpired: true, expired: true });
  assert.deepEqual(checkSessionExpired(byPerf, 0, 0), { justExpired: false, expired: true });

  const byWall = { expired: false, deadline: 50_000, expires: 20_000 };
  assert.deepEqual(checkSessionExpired(byWall, 100, 20_000), { justExpired: true, expired: true });
  assert.deepEqual(checkSessionExpired(byWall, 100, 1_000), { justExpired: false, expired: true });

  assert.equal(nextExpiryDelay(MaxTimerDelay + 5_000, 1_000), MaxTimerDelay);
  assert.equal(nextExpiryDelay(10_000, 2_500), 7_500);
});

test("saving, tombstones, monotonic times, trimming and snapshot eligibility", () => {
  for (const tc of [
    { input: "hello", want: "hello" },
    { input: "   \n\t ", want: "" },
    { input: "😀".repeat(4000), want: "😀".repeat(4000) },
    { input: "a".repeat(4001), want: "" },
  ]) {
    assert.equal(normalizeDraftText(tc.input), tc.want);
  }
  assert.equal(nextEditTime(100, 100), 101);
  assert.equal(nextEditTime(200, 100), 201);
  assert.equal(nextEditTime(100, 150), 150);
  assert.equal(nextSendEditTime(120, 150), 151);
  assert.equal(shouldWriteDraft(100, 100), false);
  assert.equal(shouldWriteDraft(105, 100), false);
  assert.equal(shouldWriteDraft(99, 100), true);
  assert.equal(shouldWriteDraft(undefined, 100), true);

  const ownKeys = Array.from({ length: 51 }, (_, i) => draftKey(memberA, sessionPage, `02000000-0000-4000-8000-${String(i).padStart(12, "0")}`, "feed"));
  const foreignKey = draftKey(memberB, sessionPage, channelA, "feed");
  const times = new Map(ownKeys.map((k, i) => [k, i === 0 || i === 1 ? 10 : 100 + i]));
  assert.deepEqual(selectKeysToTrim([foreignKey, ownKeys[1], ownKeys[0], ...ownKeys.slice(2)], scope, 49, (k) => times.get(k), ownKeys[50]), [ownKeys[0]]);
  assert.deepEqual(selectKeysToTrim([...ownKeys], scope, 50, (k) => times.get(k)), [ownKeys[0]]);

  assert.equal(isSendDetail({ key: null, body: "hi", inPlace: true }), false);
  assert.equal(isSendDetail({ key: sendKeyA, body: 5 }), false);
  assert.equal(isSendDetail(undefined), false);
  assert.equal(isSendDetail({ key: sendKeyA, body: "" }), true);
  assert.deepEqual(prepareSendSnapshot({ key: sendKeyA, body: "  ", inPlace: false }, 20, 25), {
    text: "", at: 26, source: sendKeyA, savedKey: null, sending: { key: sendKeyA, inPlace: false },
  });
  const sentSnap = prepareSendSnapshot({ key: sendKeyA, body: "sent text", inPlace: true }, 20, 15);
  assert.equal(sentSnap?.savedKey, sendKeyA);
  assert.deepEqual(matchSentSnapshot(sentSnap?.sending, sendKeyA), { key: sendKeyA, inPlace: true });
  assert.equal(matchSentSnapshot(sentSnap?.sending, sendKeyB), null);

  for (const tc of [
    { name: "matching source", source: sendKeyA, key: sendKeyA, inPlace: false, last: "v1", cur: "v1", want: true },
    { name: "in-place edited during send", source: null, key: sendKeyA, inPlace: true, last: "v1", cur: "v1", want: true },
    { name: "pending edited during send", source: null, key: sendKeyA, inPlace: false, last: "v1", cur: "v1", want: false },
    { name: "competing tab write", source: sendKeyA, key: sendKeyA, inPlace: true, last: "v1", cur: "v2", want: false },
    { name: "no prior write", source: sendKeyA, key: sendKeyA, inPlace: true, last: undefined, cur: "v1", want: false },
  ]) {
    assert.equal(canReplaceSnapshot(tc.source, tc.key, tc.inPlace, tc.last, tc.cur), tc.want, tc.name);
  }

  assert.deepEqual(pendingActionDecision({ kind: "edit", key: sendKeyA, restoredKey: sendKeyB }), { action: "save-edit", savedKey: sendKeyB });
  assert.deepEqual(pendingActionDecision({ kind: "remove", key: sendKeyA, restoredKey: null }), { action: "replace", sendKey: sendKeyA });
  assert.equal(restoredSendKey({ text: "hello", key: sendKeyA }), sendKeyA);
  assert.equal(restoredSendKey({ text: "", key: sendKeyA }), null);
  assert.equal(restoredSendKey({ text: "hello", key: "bad" }), null);
});

test("post-load malformed timestamps preserve v7 comparison, send-time and trim semantics", () => {
  assert.equal(shouldWriteDraft("200", 100), false);
  assert.equal(shouldWriteDraft("100", 100), false);
  assert.equal(shouldWriteDraft("50", 100), true);
  assert.equal(shouldWriteDraft("bad", 100), true);
  assert.equal(shouldWriteDraft(NaN, 100), true);
  assert.equal(shouldWriteDraft(-50, 100), true);
  assert.equal(shouldWriteDraft(-50, -50), false);
  assert.equal(shouldWriteDraft(undefined, 100), true);
  assert.equal(shouldWriteDraft(null, 100), true);
  assert.equal(shouldWriteDraft(null, 0), false);

  assert.equal(nextSendEditTime(120, "200"), 2001);
  assert.equal(Number.isNaN(nextSendEditTime(120, "bad")), true);
  assert.equal(nextSendEditTime(120, ""), 120);
  assert.equal(nextSendEditTime(120, NaN), 120);
  assert.equal(nextSendEditTime(120, -50), 120);
  assert.equal(nextSendEditTime(-100, -50), -49);
  assert.equal(nextSendEditTime(120, undefined), 120);
  assert.equal(nextSendEditTime(120, null), 120);

  const ownKeys = Array.from({ length: 5 }, (_, i) => draftKey(memberA, sessionPage, `02000000-0000-4000-8000-${String(i).padStart(12, "0")}`, "feed"));
  const malformedTimes = new Map([
    [ownKeys[0], undefined],
    [ownKeys[1], NaN],
    [ownKeys[2], -50],
    [ownKeys[3], "200"],
    [ownKeys[4], 100],
  ]);
  assert.deepEqual(
    selectKeysToTrim([ownKeys[3], ownKeys[4], ownKeys[1], ownKeys[0], ownKeys[2]], scope, 1, (k) => malformedTimes.get(k)),
    [ownKeys[2], ownKeys[0], ownKeys[1], ownKeys[4]],
  );
  assert.deepEqual(
    selectKeysToTrim([ownKeys[1], ownKeys[0]], scope, 1, (k) => (k === ownKeys[1] ? "bad" : 100)),
    [ownKeys[0]],
  );
});

test("adapter read preserves parsed JSON string values across trim and send", async () => {
  const capKeys = Array.from({ length: 50 }, (_, i) =>
    draftKey(memberA, sessionPage, `02000000-0000-4000-8000-${String(i + 10).padStart(12, "0")}`, "feed"),
  );
  const openKey = draftKey(memberA, sessionPage, channelA, "feed");
  const entryAt = (at) => JSON.stringify({ member: memberA, session: sessionPage, expires: scope.expires, text: "v", at, key: null });
  const storage = makeStorage([...capKeys.map((k, i) => [k, entryAt(nowMs - 1000 + i)]), [openKey, entryAt(nowMs)]]);
  await withAdapter(storage, () => nowMs, ({ body }) => {
    assert.deepEqual(storage.removed, []);
    storage[capKeys[1]] = '"abc"';
    storage[openKey] = '"abc"';
    body.dispatchEvent(new CustomEvent("message-sending", { bubbles: true, detail: { key: sendKeyA, body: "sent", inPlace: true } }));
  });
  assert.deepEqual(storage.removed, [capKeys[0]]);
  assert.equal(Object.hasOwn(storage, capKeys[0]), false);
  assert.equal(Object.hasOwn(storage, capKeys[1]), true);
  assert.equal(JSON.parse(storage.getItem(openKey)).at, null);
});

test("marker row predicate and sidebar URL parsing", () => {
  const places = new Set(["channel", "channel:topic"]);
  assert.deepEqual(
    [
      marked(places, "channel", "feed", false),
      marked(places, "channel", "topic", false),
      marked(places, "channel", "sibling", false),
      marked(places, "channel", "topic", true),
      marked(places, "other", "feed", false),
    ],
    [true, true, false, false, false],
  );
  assert.deepEqual(parseSidebarPlace(`https://example.test/organizations/acme/channels/${channelA}`), { channel: channelA, topic: "feed" });
  assert.deepEqual(parseSidebarPlace(`https://example.test/organizations/acme/channels/${channelA}/topics/${topicA}`), { channel: channelA, topic: topicA });
});

test("sending edits first: the saved time comes from the edit's clock sample and the stored time is read after it, as v7 did", async () => {
  const openKey = draftKey(memberA, sessionPage, channelA, "feed");
  let armed = false, clockReads = 0, readsBeforeStoredRead = null;
  const storage = makeStorage([], (k) => {
    if (armed && k === openKey && readsBeforeStoredRead === null) readsBeforeStoredRead = clockReads;
  });
  // Every clock read during the send is a later millisecond than the last.
  await withAdapter(storage, () => nowMs + (armed ? 1000 * clockReads++ : 0), ({ body }) => {
    body.value = "typed";
    armed = true;
    body.dispatchEvent(new CustomEvent("message-sending", { bubbles: true, detail: { key: sendKeyA, body: "sent", inPlace: false } }));
    armed = false;
  });
  // edited() reads the clock twice (expiry check, then the edit time); the stored entry is read only afterwards.
  assert.equal(readsBeforeStoredRead, 2);
  const saved = JSON.parse(storage.getItem(openKey));
  assert.equal(saved.at, nowMs + 1000);
  assert.equal(saved.text, "sent");
  assert.equal(saved.key, sendKeyA);
});

test("sending after the session expired writes nothing and removes the session's drafts, as v7 did", async () => {
  const openKey = draftKey(memberA, sessionPage, channelA, "feed");
  const entry = JSON.stringify({ member: memberA, session: sessionPage, expires: scope.expires, text: "v", at: nowMs, key: null });
  const storage = makeStorage([[openKey, entry]]);
  let armed = false;
  await withAdapter(storage, () => (armed ? scope.expires + 1 : nowMs), ({ body }) => {
    armed = true;
    body.dispatchEvent(new CustomEvent("message-sending", { bubbles: true, detail: { key: sendKeyA, body: "sent", inPlace: false } }));
    armed = false;
  });
  assert.equal(Object.hasOwn(storage, openKey), false);
  assert.deepEqual(storage.removed, [openKey]);
});
