# Stage-2 snapshot witness: capture (6a)

Part of #865 PR 3. Production does not call `conversation.Reader.WithWitness`; enabling/emission is 6b. No load runs.

Capture follows counts in the existing repeatable-read transaction: sidebar, page, members or channel list.
When installed, it adds savepoint/release, org's cursor and two unread queries. SQL errors recover through
the savepoint and discard capture: no partial result, returned error detail or logging. Broken connections
still fail outer completion. No pool-bound reader or cache is introduced.

Version 1 carries organization/member/current-channel IDs, validated selection, committed `event_seq` and persisted `joined_event_seq`.
All stored ranges for listed plus current channels are preserved (including prefix, no cap), alongside stored floors for
listed plus selected topics in the current channel. Missing floors stay absent; empty ranges mean implicit join coverage.
No message bodies or other members' state; the organization sequence does not claim rendered coverage.

[Tests](../../cmd/ribbitto/witness_test.go) gate commits between reads without sleeps and pin the pre-change off trace.
The maintainer's seven conditions for the comparison exception (#865) are:

1. Explain the nondeterminism: `internal/unread/topics.go` iterates `state.Floors`, appending IDs and floors together from a Go map.
2. Explain the SQL semantics: `CountTopicUnread` joins the two arrays by paired `WITH ORDINALITY`, preserving each ID's floor.
3. Limit the exception to that statement's arguments 7 (topic IDs) and 8 (corresponding floors).
4. Compare unordered (ID, floor) pairs, never independently sorted arrays; require equal lengths and reject duplicate IDs.
5. Keep SQL text, every other argument, statement count and statement/transaction order exact.
6. Leave production counting code and queries unchanged; this is a test-only comparison exception.
7. Keep a table-driven, database-free regression test of canonicalization and equality: paired permutations pass; reassigned floors, lost/duplicate pairs, unequal lengths and all other changes fail. Mutation-check it and record commands and actual failing assertions in the PR body.
