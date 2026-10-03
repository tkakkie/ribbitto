# 24. A cursor that cannot be served gets `reset`, never a partial replay

**Decided:** when the log cannot serve a stream's cursor, the stream sends one
`reset` instead of the affected batch, and the page reloads and takes a fresh
cursor. That is the case for a cursor below the replay boundary (expired by
retention), a cursor above the committed sequence, and a gap detected in a
batch. Ordinary filtering is unchanged: events of other channels, kinds the
stream does not deliver and events the member may not receive are passed
over on purpose, and a failed read, render or send stops the stream before
that event so the reconnect retries it. Database restores are supported only
with ribbitto stopped (stop, restore, start; see the README), so in-memory
state never describes an older timeline. Details:
[replay](../architecture/replay.md#ordering-and-replay).
**Why:** "nothing is lost on reconnect" is M3's goal; a reload is visible and
cheap, while replaying only what is left leaves the page wrong without anyone
noticing (#161, #263, #267).
**Considered:** replaying what is left after retention (silently loses
messages); supporting a rollback under a running process (needs the hub and
every cache to detect it; listed in #261).
