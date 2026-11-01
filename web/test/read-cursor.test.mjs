import test from 'node:test';
import assert from 'node:assert/strict';
import { readCursor, readDue, retryDelay, settleRead } from '../src/read-cursor.ts';

const now = 1700000000000;
const large = '9007199254740993';
const largePrev = 9007199254740992n;

for (const [name, snapshot, applied, attempted, intent, want] of [
  ['snapshot before any attempt', '42', null, null, null, '42'],
  ['snapshot before any attempt ignores newer applied cursor', '42', '100', null, null, '42'],
  ['latest applied cursor after an attempt', '42', '100', 42n, null, '100'],
  ['fallback to snapshot when no applied cursor exists', '42', null, 42n, null, '42'],
  ['fallback to snapshot when applied cursor is empty', '42', '', 42n, null, '42'],
  ['intent wins before any attempt', '42', '100', null, '99', '99'],
  ['intent wins after an attempt', '42', '105', 42n, '99', '99'],
]) test(`readCursor: ${name}`, () => {
  assert.equal(readCursor(snapshot, applied, attempted, intent), want);
});

for (const [name, cursor, succeeded, want] of [
  ['due before any success', '0', null, true],
  ['due above last success', '43', 42n, true],
  ['not due at last success', '42', 42n, false],
  ['not due below last success', '41', 42n, false],
  ['due above 2^53 as BigInt', large, largePrev, true],
  ['not due at 2^53 + 1', large, BigInt(large), false],
]) test(`readDue: ${name}`, () => {
  assert.equal(readDue(cursor, succeeded), want);
});

for (const [name, state, attempted, successful, latest, want] of [
  ['success records attempted, resets failures, and flushes',
    { succeeded: 40n, failures: 3, intent: null }, 42n, true, '42', { succeeded: 42n, failures: 0, intent: null, flush: true }],
  ['success clears intent when attempt equals intent',
    { succeeded: null, failures: 2, intent: '100' }, 100n, true, '100', { succeeded: 100n, failures: 0, intent: null, flush: true }],
  ['success clears intent when attempt exceeds intent',
    { succeeded: 42n, failures: 1, intent: '100' }, 105n, true, '105', { succeeded: 105n, failures: 0, intent: null, flush: true }],
  ['success keeps newer intent above 2^53 when attempt has not covered it',
    { succeeded: 42n, failures: 2, intent: large }, largePrev, true, large, { succeeded: largePrev, failures: 0, intent: large, flush: true }],
  ['non-429 failure clears intent without flushing same cursor',
    { succeeded: 42n, failures: 2, intent: '100' }, 100n, false, '100', { succeeded: 42n, failures: 2, intent: null, flush: false }],
  ['non-429 failure does not flush older cursor',
    { succeeded: 42n, failures: 1, intent: null }, 100n, false, '99', { succeeded: 42n, failures: 1, intent: null, flush: false }],
  ['non-429 failure flushes strictly newer work',
    { succeeded: 42n, failures: 2, intent: '100' }, 100n, false, '101', { succeeded: 42n, failures: 2, intent: null, flush: true }],
  ['non-429 failure compares cursors above 2^53 as BigInt',
    { succeeded: 42n, failures: 1, intent: large }, largePrev, false, large, { succeeded: 42n, failures: 1, intent: null, flush: true }],
  ['null attempt on success still flushes pending work',
    { succeeded: 40n, failures: 2, intent: null }, null, true, 'bad', { succeeded: null, failures: 0, intent: null, flush: true }],
  ['null attempt on non-429 failure clears intent and flushes positive cursor',
    { succeeded: null, failures: 1, intent: '100' }, null, false, '42', { succeeded: null, failures: 1, intent: null, flush: true }],
]) test(`settleRead: ${name}`, () => {
  assert.deepEqual(settleRead(state, attempted, successful, latest), want);
});

test('settleRead: preserves state transitions before BigInt throws on malformed intent or cursor', () => {
  const afterSuccess = { succeeded: null, failures: 1, intent: 'bad' };
  assert.throws(() => settleRead(afterSuccess, 42n, true, '42'), SyntaxError);
  assert.deepEqual(afterSuccess, { succeeded: 42n, failures: 0, intent: 'bad' });
  assert.equal(retryDelay(afterSuccess.failures, null, 0, now), 500);

  const afterFailure = { succeeded: 42n, failures: 1, intent: '100' };
  assert.throws(() => settleRead(afterFailure, 42n, false, 'bad'), SyntaxError);
  assert.equal(afterFailure.intent, null);
});

for (const [name, failures, retryAfter, random, want] of [
  ['first failure minimum jitter', 0, null, 0, 500],
  ['first failure maximum jitter', 0, null, 1, 1000],
  ['second failure midpoint jitter', 1, null, 0.5, 1500],
  ['third failure maximum jitter', 2, null, 1, 4000],
  ['fourth failure reaches 8 s cap', 3, null, 1, 8000],
  ['fifth failure stays capped at 8 s', 4, null, 0.5, 6000],
  ['Retry-After seconds floor wins above backoff', 0, '3', 0, 3000],
  ['Retry-After seconds floor wins above 8 s cap', 3, '12', 0, 12000],
  ['backoff wins when Retry-After seconds is shorter', 2, '1', 0.5, 3000],
  ['Retry-After HTTP-date floor wins above backoff', 1, 'Tue, 14 Nov 2023 22:13:30 GMT', 0, 10000],
  ['past HTTP-date falls back to backoff', 0, 'Tue, 14 Nov 2023 22:13:10 GMT', 0, 500],
  ['invalid Retry-After falls back to backoff', 0, 'invalid', 0, 500],
  ['empty Retry-After falls back to backoff', 0, '', 0, 500],
  ['clamps large Retry-After seconds to 2^31 - 1 ms', 0, '2592000', 0, 2147483647],
  ['clamps distant HTTP-date to 2^31 - 1 ms', 1, 'Thu, 14 Nov 2024 22:13:20 GMT', 0, 2147483647],
]) test(`retryDelay: ${name}`, () => {
  assert.equal(retryDelay(failures, retryAfter, random, now), want);
});

test('retryDelay: HTTP-date evaluates Date.parse, then Date.now, then Math.random, and numeric skips Date.now', () => {
  const origParse = Date.parse, origNow = Date.now, origRandom = Math.random;
  try {
    let clock = now;
    const calls = [];
    Date.parse = (s) => { calls.push(`parse:${s}`); return origParse(s); };
    Date.now = () => { calls.push('now'); return clock; };
    Math.random = () => { calls.push('random'); clock += 100; return 0; };
    assert.equal(retryDelay(1, 'Tue, 14 Nov 2023 22:13:30 GMT'), 10000);
    assert.deepEqual(calls, ['parse:Tue, 14 Nov 2023 22:13:30 GMT', 'now', 'random']);
    calls.length = 0;
    assert.equal(retryDelay(0, '3'), 3000);
    assert.deepEqual(calls, ['random']);
  } finally {
    Date.parse = origParse; Date.now = origNow; Math.random = origRandom;
  }
});
