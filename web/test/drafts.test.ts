import assert from "node:assert/strict";
import test from "node:test";
import {
  DraftPrefix, SignedOutNoticeKey,
  collectDraftPlaces, decideCleanupKey, draftKey, isOwnDraftKey, isSessionDraftKey, isUUID, isUUIDv7,
  isValidDraftEntry, marked, parseDraftEntry, parseDraftKey, parsePageScope, parseSidebarPlace,
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

const makeStorage = (entries: readonly (readonly [string, string])[], onGet?: (key: string) => void) => {
  const storage: Record<string, string> = Object.fromEntries(entries);
  const adapter = Object.assign(storage, {
    getItem(key: string) {
      onGet?.(key);
      return storage[key] ?? null;
    },
    removeItem(key: string) { delete storage[key]; },
  });
  // sweepKeys enumerates stored keys, as it does on browser Storage.
  Object.defineProperties(adapter, { getItem: { enumerable: false }, removeItem: { enumerable: false } });
  return adapter;
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
    { name: "unrelated key", key: SignedOutNoticeKey, horizon: sessionPage, member: memberA, want: "ignore" },
    { name: "malformed key", key: malformedKey, horizon: sessionPage, member: memberA, want: "remove" },
    { name: "foreign member in own session", key: sameSessionForeignMember, horizon: sessionPage, member: memberA, want: "remove" },
    { name: "own entry", key: ownValidKey, horizon: sessionPage, member: memberA, want: "validate" },
    { name: "older foreign session", key: olderForeign, horizon: sessionPage, member: memberA, want: "remove" },
    { name: "same millisecond foreign session", key: sameMsForeign, horizon: sessionPage, member: memberA, want: "keep" },
    { name: "newer foreign session", key: newerForeign, horizon: sessionPage, member: memberA, want: "keep" },
    { name: "sign-in keeps same millisecond", key: ownValidKey, horizon: sessionSameMs, member: undefined, want: "keep" },
    { name: "sign-in removes older session", key: olderForeign, horizon: sessionSameMs, member: undefined, want: "remove" },
  ]) {
    assert.equal(decideCleanupKey(tc.key, tc.horizon, tc.member), tc.want, tc.name);
  }

  const validValue = JSON.stringify({ member: memberA, session: sessionPage, expires: scope.expires, text: "hi", at: nowMs, key: null });
  const seed: [string, string][] = [
    [SignedOutNoticeKey, sessionPage],
    [malformedKey, validValue],
    [sameSessionForeignMember, validValue],
    [ownValidKey, validValue],
    [ownInvalidKey, "{broken"],
    [olderForeign, validValue],
    [sameMsForeign, validValue],
    [newerForeign, validValue],
  ];
  const orgStorage = makeStorage(seed, (key) => {
    if (key !== ownValidKey && key !== ownInvalidKey) throw new Error("foreign value read");
  });
  sweepKeys(orgStorage, sessionPage, scope, nowMs);
  assert.deepEqual(Object.keys(orgStorage), [SignedOutNoticeKey, ownValidKey, sameMsForeign, newerForeign]);

  const signInStorage = makeStorage(seed, () => { throw new Error("sign-in value read"); });
  sweepKeys(signInStorage, sessionPage, null, nowMs);
  assert.deepEqual(Object.keys(signInStorage), [SignedOutNoticeKey, sameSessionForeignMember, ownValidKey, ownInvalidKey, sameMsForeign, newerForeign]);

  const values = new Map(seed);
  assert.deepEqual([...collectDraftPlaces([ownValidKey, sameMsForeign, newerForeign], scope, nowMs, (key) => {
    if (key !== ownValidKey) throw new Error("foreign value read");
    return values.get(key) ?? null;
  })], [channelA, `${channelA}:feed`]);
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
  assert.equal(Object.hasOwn(storage, ownKey), true);
  assert.equal(Object.hasOwn(storage, laterMalformed), true);
});

test("cleanup applies retention bounds using an injected clock", () => {
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
  let clock = nowMs;
  const directStorage = makeStorage([
    [brokenKey, "{broken"],
    [firstKey, boundaryValue],
    [secondKey, boundaryValue],
  ], (key) => { if (key === secondKey) clock += 1; });
  sweepKeys(directStorage, sessionPage, scope, () => clock);
  for (const row of [
    { name: "invalid JSON", key: brokenKey, want: false },
    { name: "younger than seven days", key: firstKey, want: true },
    { name: "seven days old", key: secondKey, want: false },
  ]) {
    assert.equal(Object.hasOwn(directStorage, row.key), row.want, row.name);
  }
});

test("draft places exclude invalid, foreign and expired entries using an injected clock", () => {
  const channelB = "02000000-0000-4000-8000-000000000002";
  const brokenKey = draftKey(memberA, sessionPage, "02000000-0000-4000-8000-000000000000");
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
  const directValues = new Map([
    [foreignKey, boundaryValue],
    [brokenKey, "{broken"],
    [firstKey, boundaryValue],
    [secondKey, boundaryValue],
  ]);
  let clock = nowMs;
  const directPlaces = collectDraftPlaces(
    [foreignKey, brokenKey, firstKey, secondKey],
    scope,
    () => clock,
    (key) => { if (key === secondKey) clock += 1; return directValues.get(key) ?? null; },
  );
  assert.deepEqual([...directPlaces], [channelB, `${channelB}:feed`]);
  for (const row of [
    { name: "channel with fresh feed", place: channelB, want: true },
    { name: "fresh feed", place: `${channelB}:feed`, want: true },
    { name: "expired topic", place: `${channelB}:${topicA}`, want: false },
    { name: "invalid entry", place: "02000000-0000-4000-8000-000000000000", want: false },
  ]) assert.equal(directPlaces.has(row.place), row.want, row.name);
});

test("page scope parses integer bounds", () => {
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
  for (const row of [
    { name: "non-v7 session", member: memberA, session: memberA, expires: "100", rendered: "50" },
    { name: "null expiry", member: memberA, session: sessionPage, expires: null, rendered: "50" },
    { name: "null rendered", member: memberA, session: sessionPage, expires: "100", rendered: null },
    { name: "missing expiry", member: memberA, session: sessionPage, expires: undefined, rendered: "50" },
    { name: "missing rendered", member: memberA, session: sessionPage, expires: "100", rendered: undefined },
    { name: "fractional expiry", member: memberA, session: sessionPage, expires: "1.5", rendered: "50" },
  ]) {
    assert.equal(parsePageScope(row), null, row.name);
  }
});

test("draft entries validate ownership, text and retention bounds", () => {
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
