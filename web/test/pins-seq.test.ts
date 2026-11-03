import test from "node:test";
import assert from "node:assert/strict";
import { isControlPinned, isStalePinsSeq } from "../src/pins-seq.ts";

test('isStalePinsSeq: rejects only older sequences with BigInt precision', () => {
  const cases = [
    { name: "absent section (null)", incomingSeq: "41", currentSeq: null, want: false },
    { name: "absent section (undefined)", incomingSeq: "41", currentSeq: undefined, want: false },
    { name: "absent section (empty)", incomingSeq: "41", currentSeq: "", want: false },
    { name: "missing incoming sequence", incomingSeq: null, currentSeq: "42", want: false },
    { name: "older sequence", incomingSeq: "41", currentSeq: "42", want: true },
    { name: "zero below positive", incomingSeq: "0", currentSeq: "1", want: true },
    { name: "equal sequence", incomingSeq: "42", currentSeq: "42", want: false },
    { name: "equal zero sequence", incomingSeq: "0", currentSeq: "0", want: false },
    { name: "newer sequence", incomingSeq: "43", currentSeq: "42", want: false },
    { name: "older above 2^53 (BigInt precision)", incomingSeq: "9007199254740992", currentSeq: "9007199254740993", want: true },
    { name: "equal above 2^53 (BigInt precision)", incomingSeq: "9007199254740993", currentSeq: "9007199254740993", want: false },
    { name: "newer above 2^53 (BigInt precision)", incomingSeq: "9007199254740994", currentSeq: "9007199254740993", want: false },
  ];
  for (const row of cases) {
    assert.equal(isStalePinsSeq(row.incomingSeq, row.currentSeq), row.want, row.name);
  }
});

test('isControlPinned: requires a complete ID in the space-separated list', () => {
  const cases = [
    { name: "empty list", pinnedIDs: "", controlID: "msg-1", want: false },
    { name: "empty list and empty control id", pinnedIDs: "", controlID: "", want: false },
    { name: "null list", pinnedIDs: null, controlID: "msg-1", want: false },
    { name: "single id match", pinnedIDs: "msg-1", controlID: "msg-1", want: true },
    { name: "single id miss", pinnedIDs: "msg-1", controlID: "msg-2", want: false },
    { name: "several ids first match", pinnedIDs: "msg-1 msg-2 msg-3", controlID: "msg-1", want: true },
    { name: "several ids middle match", pinnedIDs: "msg-1 msg-2 msg-3", controlID: "msg-2", want: true },
    { name: "several ids last match", pinnedIDs: "msg-1 msg-2 msg-3", controlID: "msg-3", want: true },
    { name: "several ids miss", pinnedIDs: "msg-1 msg-2 msg-3", controlID: "msg-4", want: false },
    { name: "prefix of single id", pinnedIDs: "msg-10", controlID: "msg-1", want: false },
    { name: "suffix of single id", pinnedIDs: "sub-msg-1", controlID: "msg-1", want: false },
    { name: "prefix of id in list", pinnedIDs: "msg-10 msg-20", controlID: "msg-1", want: false },
    { name: "suffix of id in list", pinnedIDs: "msg-10 msg-20", controlID: "20", want: false },
  ];
  for (const row of cases) {
    assert.equal(isControlPinned(row.pinnedIDs, row.controlID), row.want, row.name);
  }
});
