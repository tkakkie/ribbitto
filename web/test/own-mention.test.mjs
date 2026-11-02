import test from "node:test";
import assert from "node:assert/strict";
import { isOwnMention } from "../src/own-mention.ts";

test("isOwnMention requires a non-empty stream data-member equal to the element data-member-id", () => {
  const memberA = "01950000-0000-7000-8000-00000000000a";
  const memberB = "01950000-0000-7000-8000-00000000000b";
  const cases = [
    { name: "matching non-empty member IDs", streamMember: memberA, mentionMemberID: memberA, want: true },
    { name: "different member IDs", streamMember: memberA, mentionMemberID: memberB, want: false },
    { name: "empty stream member and empty mention ID", streamMember: "", mentionMemberID: "", want: false },
    { name: "empty stream member and valid mention ID", streamMember: "", mentionMemberID: memberA, want: false },
    { name: "null stream member (missing attribute)", streamMember: null, mentionMemberID: memberA, want: false },
    { name: "undefined stream member (missing stream)", streamMember: undefined, mentionMemberID: memberA, want: false },
    { name: "null stream member and null mention ID", streamMember: null, mentionMemberID: null, want: false },
    { name: "undefined stream member and undefined mention ID", streamMember: undefined, mentionMemberID: undefined, want: false },
    { name: "valid stream member and empty mention ID", streamMember: memberA, mentionMemberID: "", want: false },
    { name: "valid stream member and null mention ID", streamMember: memberA, mentionMemberID: null, want: false },
    { name: "valid stream member and undefined mention ID", streamMember: memberA, mentionMemberID: undefined, want: false },
    { name: "case-different member IDs do not match", streamMember: memberA, mentionMemberID: memberA.toUpperCase(), want: false },
  ];
  for (const tc of cases) {
    assert.equal(isOwnMention(tc.streamMember, tc.mentionMemberID), tc.want, tc.name);
  }
});
