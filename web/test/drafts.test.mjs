import assert from "node:assert/strict";
import test from "node:test";
import {
  DraftPrefix, MaxTimerDelay, SignedOutNoticeKey, canReplaceSnapshot, checkSessionExpired,
  collectDraftPlaces, decideCleanupKey, draftKey, isOwnDraftKey, isSessionDraftKey, isUUID, isUUIDv7,
  isValidDraftEntry, marked, matchSentSnapshot, nextEditTime, nextExpiryDelay, nextSendEditTime,
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
  const values = new Map([
    [ownValidKey, validValue],
    [ownInvalidKey, "{broken"],
    [sameSessionForeignMember, validValue],
    [olderForeign, validValue],
    [sameMsForeign, validValue],
    [newerForeign, validValue],
  ]);
  const readKeys = [];
  const readRaw = (k) => { readKeys.push(k); return values.get(k) ?? null; };
  const keys = [SignedOutNoticeKey, malformedKey, sameSessionForeignMember, ownValidKey, ownInvalidKey, olderForeign, sameMsForeign, newerForeign];

  assert.deepEqual(sweepKeys(keys, sessionPage, scope, nowMs, readRaw), [malformedKey, sameSessionForeignMember, ownInvalidKey, olderForeign]);
  assert.deepEqual(readKeys, [ownValidKey, ownInvalidKey]);

  const signInReads = [];
  assert.deepEqual(sweepKeys(keys, sessionPage, null, nowMs, (k) => { signInReads.push(k); return null; }), [malformedKey, olderForeign]);
  assert.deepEqual(signInReads, []);
  assert.deepEqual([...collectDraftPlaces([ownValidKey, sameMsForeign, newerForeign], scope, nowMs, readRaw)], [channelA, `${channelA}:feed`]);
  assert.deepEqual(readKeys, [ownValidKey, ownInvalidKey, ownValidKey]);
});

test("page scope and entry validity bounds", () => {
  assert.deepEqual(parsePageScope({ member: memberA, session: sessionPage, expires: String(scope.expires), rendered: String(scope.rendered) }), scope);
  for (const bad of [
    { member: memberA, session: memberA, expires: "100", rendered: "50" },
    { member: memberA, session: sessionPage, expires: null, rendered: "50" },
    { member: memberA, session: sessionPage, expires: "100", rendered: "" },
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
  assert.equal(nextSendEditTime(100, 120, 150), 151);
  assert.equal(shouldWriteDraft(100, 100), false);
  assert.equal(shouldWriteDraft(105, 100), false);
  assert.equal(shouldWriteDraft(99, 100), true);
  assert.equal(shouldWriteDraft(undefined, 100), true);

  const ownKeys = Array.from({ length: 51 }, (_, i) => draftKey(memberA, sessionPage, `02000000-0000-4000-8000-${String(i).padStart(12, "0")}`, "feed"));
  const foreignKey = draftKey(memberB, sessionPage, channelA, "feed");
  const times = new Map(ownKeys.map((k, i) => [k, i === 0 || i === 1 ? 10 : 100 + i]));
  assert.deepEqual(selectKeysToTrim([foreignKey, ownKeys[1], ownKeys[0], ...ownKeys.slice(2)], scope, 49, (k) => times.get(k), ownKeys[50]), [ownKeys[0]]);
  assert.deepEqual(selectKeysToTrim([...ownKeys], scope, 50, (k) => times.get(k)), [ownKeys[0]]);

  assert.equal(prepareSendSnapshot({ key: null, body: "hi", inPlace: true }, 10, 20, 0), null);
  assert.deepEqual(prepareSendSnapshot({ key: sendKeyA, body: "  ", inPlace: false }, 10, 20, 25), {
    text: "", at: 26, source: sendKeyA, savedKey: null, sending: { key: sendKeyA, inPlace: false },
  });
  const sentSnap = prepareSendSnapshot({ key: sendKeyA, body: "sent text", inPlace: true }, 10, 20, 15);
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
