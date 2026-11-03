import test from 'node:test';
import assert from 'node:assert/strict';
import { advancesCursor, shownCursor, admitsMove, higherSequence, announcementOverflow,
  resetHeld, resetDelay, shouldParkSource, restoreSource } from '../src/stream-cursor.ts';

for (const [incoming, current, advances] of [
  ['41', '42', false], ['42', '42', true], ['43', '42', true],
  ['9007199254740992', '9007199254740993', false],
  ['9007199254740993', '9007199254740992', true],
  ['', '', true], [' 42 ', '42', true], ['0x2b', '42', true],
]) test(`transport/applied cursor ${incoming} against ${current}`, () => {
  assert.equal(advancesCursor(incoming, current), advances);
});
for (const [incoming, current, error] of [
  ['bad', '42', SyntaxError], ['43', 'bad', SyntaxError],
  ['1.5', '42', SyntaxError], ['43', undefined, TypeError], ['43', null, TypeError],
]) test(`malformed cursor ${incoming} against ${current}`, () => {
  assert.throws(() => advancesCursor(incoming, current), error);
});
for (const visible of [false, true]) {
  for (const applied of [undefined, '', '42', '9007199254740993', 'bad']) {
    test(`shown visible=${visible}, applied=${applied}`, () => {
      assert.equal(shownCursor(visible, () => applied), visible && applied ? applied : undefined);
    });
  }
}
test('hidden shown-cursor decision never reads a throwing cursor', () => {
  assert.equal(shownCursor(false, () => { throw new Error('cursor read'); }), undefined);
});
for (const [sequence, boundary, admitted] of [
  ['0', 0n, true], ['1', 0n, true], ['39', 40n, false], ['40', 40n, true], ['41', 40n, true],
  ['9007199254740992', 9007199254740993n, false], ['9007199254740993', 9007199254740993n, true],
]) test(`move ${sequence} at boundary ${boundary}`, () => {
  assert.equal(admitsMove(sequence, boundary), admitted);
});
for (const [child, incoming, higher] of [
  ['39', '40', false], ['40', '40', false], ['41', '40', true],
  ['9007199254740993', '9007199254740992', true],
]) test(`insertion before ${child} for ${incoming}`, () => {
  assert.equal(higherSequence(child, incoming), higher);
});
test('moved insertion selects first higher child, including zero boundary', () => {
  const children = ['20', '40', '60'];
  for (const [seq, expected] of [['10', '20'], ['40', '60'], ['61', undefined]]) {
    assert.equal(admitsMove(seq, 0n), true);
    assert.equal(children.find(child => higherSequence(child, seq)), expected);
  }
  assert.throws(() => admitsMove(undefined, 0n), TypeError);
  assert.throws(() => admitsMove('bad', 0n), SyntaxError);
  assert.throws(() => higherSequence(undefined, '1'), TypeError);
  assert.throws(() => higherSequence('2', 'bad'), SyntaxError);
});
for (const count of [0, 1, 9, 10, 11, 20]) test(`announcements ${count}`, () => {
  let retained = count;
  while (announcementOverflow(retained)) retained--;
  assert.equal(retained, Math.min(count, 10));
});
for (const request of [undefined, false, true]) {
  for (const alert of [false, true]) {
    for (const pending of [false, true]) test(`reset request=${request}, alert=${alert}, pending=${pending}`, () => {
      assert.equal(resetHeld(request, alert, pending), Boolean(request || alert || pending));
    });
  }
}
for (const [random, delay] of [[0, 0], [0.5, 2500], [0.999999, 4999.995]]) {
  test(`reset jitter ${random}`, () => assert.equal(resetDelay(random), delay));
}
for (const hiding of [false, true]) {
  for (const readyState of [0, 1, 2]) test(`park hiding=${hiding}, source=${readyState}`, () => {
    const source = { readyState };
    assert.equal(shouldParkSource(hiding, source.readyState), hiding && readyState !== 2);
  });
}
test('persisted restore dispatches once; nonpersisted show retains parking', () => {
  const source = { readyState: 1 };
  let parked = shouldParkSource(true, source.readyState) ? source : null, errors = 0;
  for (const persisted of [false, true, true]) {
    const restore = restoreSource(parked, persisted);
    if (restore) { assert.equal(restore, source); errors++; parked = null; }
  }
  assert.equal(errors, 1);
  assert.equal(restoreSource(shouldParkSource(true, 2) ? source : null, true), null);
});
