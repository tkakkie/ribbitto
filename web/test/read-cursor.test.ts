import test from 'node:test';
import assert from 'node:assert/strict';
import { readCursor, readDue, retryDelay, settleRead, type ReadState } from '../src/read-cursor.ts';

const now = 1700000000000;
const large = '9007199254740993';
const largePrev = 9007199254740992n;

test('readCursor: selects intent, snapshot or latest applied cursor', () => {
  const cases = [
    { name: 'snapshot before any attempt', snapshot: '42', applied: null, attempted: null, intent: null, want: '42' },
    { name: 'snapshot before any attempt ignores newer applied cursor', snapshot: '42', applied: '100', attempted: null, intent: null, want: '42' },
    { name: 'latest applied cursor after an attempt', snapshot: '42', applied: '100', attempted: 42n, intent: null, want: '100' },
    { name: 'fallback to snapshot when no applied cursor exists', snapshot: '42', applied: null, attempted: 42n, intent: null, want: '42' },
    { name: 'fallback to snapshot when applied cursor is empty', snapshot: '42', applied: '', attempted: 42n, intent: null, want: '42' },
    { name: 'intent wins before any attempt', snapshot: '42', applied: '100', attempted: null, intent: '99', want: '99' },
    { name: 'intent wins after an attempt', snapshot: '42', applied: '105', attempted: 42n, intent: '99', want: '99' },
  ];
  for (const row of cases) {
    assert.equal(readCursor(row.snapshot, row.applied, row.attempted, row.intent), row.want, row.name);
  }
});

test('readDue: advances only beyond the last success with BigInt precision', () => {
  const cases = [
    { name: 'due before any success', cursor: '0', succeeded: null, want: true },
    { name: 'due above last success', cursor: '43', succeeded: 42n, want: true },
    { name: 'not due at last success', cursor: '42', succeeded: 42n, want: false },
    { name: 'not due below last success', cursor: '41', succeeded: 42n, want: false },
    { name: 'due above 2^53 as BigInt', cursor: large, succeeded: largePrev, want: true },
    { name: 'not due at 2^53 + 1', cursor: large, succeeded: BigInt(large), want: false },
  ];
  for (const row of cases) {
    assert.equal(readDue(row.cursor, row.succeeded), row.want, row.name);
  }
});

test('settleRead: records success or flushes strictly newer work after failure', () => {
  const cases = [
    { name: 'success records attempted, resets failures, and flushes', state: { succeeded: 40n, failures: 3, intent: null }, attempted: 42n, successful: true, latest: '42', want: { succeeded: 42n, failures: 0, intent: null, flush: true } },
    { name: 'success clears intent when attempt equals intent', state: { succeeded: null, failures: 2, intent: '100' }, attempted: 100n, successful: true, latest: '100', want: { succeeded: 100n, failures: 0, intent: null, flush: true } },
    { name: 'success clears intent when attempt exceeds intent', state: { succeeded: 42n, failures: 1, intent: '100' }, attempted: 105n, successful: true, latest: '105', want: { succeeded: 105n, failures: 0, intent: null, flush: true } },
    { name: 'success keeps newer intent above 2^53 when attempt has not covered it', state: { succeeded: 42n, failures: 2, intent: large }, attempted: largePrev, successful: true, latest: large, want: { succeeded: largePrev, failures: 0, intent: large, flush: true } },
    { name: 'non-429 failure clears intent without flushing same cursor', state: { succeeded: 42n, failures: 2, intent: '100' }, attempted: 100n, successful: false, latest: '100', want: { succeeded: 42n, failures: 2, intent: null, flush: false } },
    { name: 'non-429 failure does not flush older cursor', state: { succeeded: 42n, failures: 1, intent: null }, attempted: 100n, successful: false, latest: '99', want: { succeeded: 42n, failures: 1, intent: null, flush: false } },
    { name: 'non-429 failure flushes strictly newer work', state: { succeeded: 42n, failures: 2, intent: '100' }, attempted: 100n, successful: false, latest: '101', want: { succeeded: 42n, failures: 2, intent: null, flush: true } },
    { name: 'non-429 failure compares cursors above 2^53 as BigInt', state: { succeeded: 42n, failures: 1, intent: large }, attempted: largePrev, successful: false, latest: large, want: { succeeded: 42n, failures: 1, intent: null, flush: true } },
    { name: 'null attempt on success still flushes pending work', state: { succeeded: 40n, failures: 2, intent: null }, attempted: null, successful: true, latest: 'bad', want: { succeeded: null, failures: 0, intent: null, flush: true } },
    { name: 'null attempt on non-429 failure clears intent and flushes positive cursor', state: { succeeded: null, failures: 1, intent: '100' }, attempted: null, successful: false, latest: '42', want: { succeeded: null, failures: 1, intent: null, flush: true } },
  ];
  for (const row of cases) {
    assert.deepEqual(settleRead(row.state, row.attempted, row.successful, row.latest), row.want, row.name);
  }
});

test('settleRead: preserves state transitions before BigInt throws on malformed intent or cursor', () => {
  const afterSuccess: ReadState = { succeeded: null, failures: 1, intent: 'bad' };
  assert.throws(() => settleRead(afterSuccess, 42n, true, '42'), SyntaxError);
  assert.deepEqual(afterSuccess, { succeeded: 42n, failures: 0, intent: 'bad' });
  assert.equal(retryDelay(afterSuccess.failures, null, 0, now), 500);

  const afterFailure = { succeeded: 42n, failures: 1, intent: '100' };
  assert.throws(() => settleRead(afterFailure, 42n, false, 'bad'), SyntaxError);
  assert.equal(afterFailure.intent, null);
});

test('retryDelay: bounds jittered backoff and honours Retry-After', () => {
  const cases = [
    { name: 'first failure minimum jitter', failures: 0, retryAfter: null, random: 0, want: 500 },
    { name: 'first failure maximum jitter', failures: 0, retryAfter: null, random: 1, want: 1000 },
    { name: 'second failure midpoint jitter', failures: 1, retryAfter: null, random: 0.5, want: 1500 },
    { name: 'third failure maximum jitter', failures: 2, retryAfter: null, random: 1, want: 4000 },
    { name: 'fourth failure reaches 8 s cap', failures: 3, retryAfter: null, random: 1, want: 8000 },
    { name: 'fifth failure stays capped at 8 s', failures: 4, retryAfter: null, random: 0.5, want: 6000 },
    { name: 'Retry-After seconds floor wins above backoff', failures: 0, retryAfter: '3', random: 0, want: 3000 },
    { name: 'Retry-After seconds floor wins above 8 s cap', failures: 3, retryAfter: '12', random: 0, want: 12000 },
    { name: 'backoff wins when Retry-After seconds is shorter', failures: 2, retryAfter: '1', random: 0.5, want: 3000 },
    { name: 'Retry-After HTTP-date floor wins above backoff', failures: 1, retryAfter: 'Tue, 14 Nov 2023 22:13:30 GMT', random: 0, want: 10000 },
    { name: 'past HTTP-date falls back to backoff', failures: 0, retryAfter: 'Tue, 14 Nov 2023 22:13:10 GMT', random: 0, want: 500 },
    { name: 'invalid Retry-After falls back to backoff', failures: 0, retryAfter: 'invalid', random: 0, want: 500 },
    { name: 'empty Retry-After falls back to backoff', failures: 0, retryAfter: '', random: 0, want: 500 },
    { name: 'clamps large Retry-After seconds to 2^31 - 1 ms', failures: 0, retryAfter: '2592000', random: 0, want: 2147483647 },
    { name: 'clamps distant HTTP-date to 2^31 - 1 ms', failures: 1, retryAfter: 'Thu, 14 Nov 2024 22:13:20 GMT', random: 0, want: 2147483647 },
  ];
  for (const row of cases) {
    assert.equal(retryDelay(row.failures, row.retryAfter, row.random, now), row.want, row.name);
    assert.equal(retryDelay(row.failures, row.retryAfter, () => row.random, () => now), row.want, row.name);
  }
});
