import test from "node:test";
import assert from "node:assert/strict";
import { isControlPinned, isStalePinsSeq } from "../src/pins-seq.ts";

for (const [name, incomingSeq, currentSeq, want] of [
  ["absent section (null)", "41", null, false],
  ["absent section (undefined)", "41", undefined, false],
  ["absent section (empty)", "41", "", false],
  ["missing incoming sequence", null, "42", false],
  ["older sequence", "41", "42", true],
  ["zero below positive", "0", "1", true],
  ["equal sequence", "42", "42", false],
  ["equal zero sequence", "0", "0", false],
  ["newer sequence", "43", "42", false],
  ["older above 2^53 (BigInt precision)", "9007199254740992", "9007199254740993", true],
  ["equal above 2^53 (BigInt precision)", "9007199254740993", "9007199254740993", false],
  ["newer above 2^53 (BigInt precision)", "9007199254740994", "9007199254740993", false],
]) {
  test(`isStalePinsSeq: ${name}`, () => {
    assert.equal(isStalePinsSeq(incomingSeq, currentSeq), want);
  });
}

for (const [name, pinnedIDs, controlID, want] of [
  ["empty list", "", "msg-1", false],
  ["empty list and empty control id", "", "", false],
  ["null list", null, "msg-1", false],
  ["single id match", "msg-1", "msg-1", true],
  ["single id miss", "msg-1", "msg-2", false],
  ["several ids first match", "msg-1 msg-2 msg-3", "msg-1", true],
  ["several ids middle match", "msg-1 msg-2 msg-3", "msg-2", true],
  ["several ids last match", "msg-1 msg-2 msg-3", "msg-3", true],
  ["several ids miss", "msg-1 msg-2 msg-3", "msg-4", false],
  ["prefix of single id", "msg-10", "msg-1", false],
  ["suffix of single id", "sub-msg-1", "msg-1", false],
  ["prefix of id in list", "msg-10 msg-20", "msg-1", false],
  ["suffix of id in list", "msg-10 msg-20", "20", false],
]) {
  test(`isControlPinned: ${name}`, () => {
    assert.equal(isControlPinned(pinnedIDs, controlID), want);
  });
}
