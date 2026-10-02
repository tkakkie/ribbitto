# 24. A cursor that cannot be served gets `reset`, never a partial replay

**Decided:** a stream either delivers every event after the client's cursor
or sends one `reset`, after which the page reloads and takes a fresh cursor.
`reset` covers a cursor below the replay boundary (expired by retention), a
cursor above the committed sequence, and a gap in a batch. Nothing is ever
skipped silently. Database restores are supported only with ribbitto stopped
(stop, restore, start; see the README), so in-memory state never describes an
older timeline. Details:
[streaming](../architecture/streaming.md#ordering-and-replay).
**Why:** "nothing is lost on reconnect" is M3's goal; a reload is visible and
cheap, while a partial replay leaves the page wrong without anyone noticing
(#161, #263, #267).
**Considered:** replaying what is left after retention (silently loses
messages); supporting a rollback under a running process (needs the hub and
every cache to detect it; listed in #261).
