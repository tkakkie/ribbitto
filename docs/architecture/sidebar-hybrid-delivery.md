# Hybrid sidebar delivery verification (#961, part 2c-b1)

The external realtime lifecycle harness connects the
[coverage guard](sidebar-hybrid-coverage.md) to `Hub.Register`, `Stream.Run`,
`RecountScheduler`, an actual `unread.Changes` watch and `Sender.Send`.
It reuses the cost writer's scoped authorization/render/send probe and persisted
conversation fixtures. Recounts use the hybrid full-model adapter; only the writer
installs model witnesses. Payloads are test tokens, not browser HTML.

Computation and render gates hold work across delivery boundaries. `synctest`
advances virtual deadlines without sleeps. Assertions distinguish another
audience's denial (skip and advance) from bound-member revocation (stop before
advancing), require fresh bound authorization immediately before each send,
preserve durable order and id-free sidebar frames, and suppress pending output
after render or completion revocation.

Disconnect cancels the held computation context and releases both the member
watch and hub registration before the uncooperative computation returns.
Reconnect starts request/watch generations anew, takes a fresh full snapshot,
and sends while old work is still held. Releasing that old work cannot send or
pollute the new stream; the coverage guard also rejects its old incarnation.

Part [2c-b2](sidebar-hybrid-progress.md) reuses this harness for continuous-posting
recount/repair progress and full-model equality at pinned persisted boundaries.
Mutation evidence belongs in the PR body. Production code, `SidebarInterval` and
#865's PR 3 plan are unchanged; measurement and adoption remain #952. Together
with 2a–2c-b2, the tests cover #961's acceptance conditions.
