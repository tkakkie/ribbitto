# Shared reader: growth and implementation

*Planned* (#232). The [shared reader](shared-reader.md) is the first step
of #236's incremental path, not its final shape. Each step below keeps the
window, the hand-over at the cursor, one render per language, authorization
immediately before each send and decision 23. Each is taken only when a
measurement calls for it; #236 judges the steps after these.

## A channel interest index

With or without the reader, every advance wakes every connection of the
organisation, and those following other channels skip the event before
rendering. #296 gives each tab one organisation-wide stream whose
connection declares several interests: the sidebar and unread counts for
the organisation, presence, and typing and messages for its channel or
topic. What changes:

- **Subscription:** a set of interests; the filter matches any of them. An
  interest is a pre-filter that grants nothing; `MayReceive` still decides.
- **Reader:** per interest, the highest sequence that touches it (from the
  envelope's channel and routing topics; organisation-wide kinds under one
  interest), each with its own close-and-replace channel, instead of one
  channel for the organisation; an index of the window's events by
  interest; and per interest the highest sequence evicted (by count or by
  the floor) that touched it. A connection registers its interests when it
  acquires the reader.
- **Wait:** until the highest level among the connection's interests passes
  its cursor. It is still a level, so no wakeup is lost; #296's ephemeral
  generation is one more level of the same kind.
- **Read:** a connection may skip events without reading them only with a
  proof, checked in the same snapshot of the index as the skip. The proof
  has three parts:
  - its original cursor is at least the window's floor, so decision 24
    still holds and the cursor is not expired;
  - the cursor is at least the reader's starting sequence;
  - for each of its interests, the highest evicted sequence is at or below
    the cursor.

  Then every event of its interests above the cursor is in the index, and
  it moves its cursor to just before the first one (today's skip and
  advance). Without the proof it reads the database as before: an expired
  cursor gets `reset`, and an evicted event of its interests is read again.
  Examples:
  - with event 101 of its channel evicted and 200 indexed, a cursor at 100
    has no proof, so 101 comes from the database;
  - with the reader started at 100, the cursor at 100 and retention
    expiring unrelated events through 150, the floor rises above the
    cursor, so the connection gets `reset` instead of skipping to 200.

  The index's issue owns a deterministic regression test for each case.

The window, the hand-over, renders, authorization and #209's cursor rules
do not change. Evidence for it: a `TestStreamCost` variant with posts spread
over many channels; today's benchmark follows one channel, so it cannot
show the wakes the index removes.

## Replay that cannot starve live delivery

Replay (the database path) shares the pool, and the event and render
caches' 16 loaders each, with live delivery. In a reconnect storm (#219)
every stream replays at once, and the reader's read and the renders of new
events queue behind replay work. What changes:

- **A limit on concurrent replays:** a process-wide FIFO semaphore taken
  before each database-path read and released once that batch is
  delivered. A connection waiting for a slot holds no pool connection and
  still sends heartbeats. The reader and window reads never take one; they
  need none, since a window read runs no query, not even a bounds check
  ([floor](shared-reader.md#the-reader)). A storm of cursors inside the
  window therefore adds no event reads or bounds checks; its renders go
  through the render cache, like live ones.
- **Separate render loaders:** renders of events inside the window load
  from their own budget, so replay renders cannot hold every slot.

The reader, the window and the cursor rules do not change; a replay only
waits longer. Evidence for it: #219's [restart harness](load-restart.md)
showing streams that are already live slowing while others replay.

## Implementation issues

Ordered; each about one pull request with its own tests and with the
documentation of what it changes (`AGENTS.md`), `high` risk
(`internal/realtime/**`). None starts before the maintainer decides to
build the reader ([Deciding](shared-reader.md#deciding-whether-to-build-it)).
Production changes only with the fourth; until then a stream without
readers runs today's path, which also stays selectable for the benchmark.

1. **The reader and its window:** registry, lifecycle, loop, count bound,
   floor, failure and shutdown; no stream uses it yet. Package
   documentation. Tier A.
2. **Fan-out to caught-up streams:** a stream inside the window reads from
   it and waits on the reader; a mixed-language test; #209's rules on that
   path, including a reader failing mid-batch; a slow stream blocks
   nothing. Tier A.
3. **The hand-over:** the per-read choice between window and database and
   the wait on the reader only, with deterministic gap, duplicate and
   expired-cursor tests. Tier A.
4. **Wiring:** `cmd/ribbitto` builds the registry, wraps retention's
   boundary and waits for the readers at shutdown, and streams use it. It
   moves the built parts out of *planned* and updates
   [streaming](streaming.md), [replay](replay.md), [real time](realtime.md),
   [stream limits](stream-limits.md) and decision 23. Tier A.
5. **Measurement:** `TestStreamCost` runs on the reader and records its
   results, with the distinct-members case, in [stream cost](stream-cost.md)
   next to #227's, plus any remaining consolidation. Tier B.

The issue numbers are added here once they are filed.
