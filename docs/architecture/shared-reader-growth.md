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
  channel for the organisation, and an index of the window's events by
  interest. A connection registers its interests when it acquires the
  reader.
- **Wait:** until the highest level among the connection's interests passes
  its cursor. It is still a level, so no wakeup is lost; #296's ephemeral
  generation is one more level of the same kind.
- **Read:** a connection that wakes finds, in the index, the first event
  above its cursor that touches its interests. If that event is still in
  the window, it moves its cursor to just before it without reading the
  events in between, which touch none of its interests (today's skip and
  advance, done from the index); otherwise it reads the database, as
  before. A connection that sleeps through many other channels' posts
  therefore does not fall below `lo` for them.

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
  still sends heartbeats; the reader and window reads never take one.
- **Separate render loaders:** renders of events inside the window load
  from their own budget, so replay renders cannot hold every slot.

The reader, the window and the cursor rules do not change; a replay only
waits longer. Evidence for it: #219's [restart harness](load-restart.md)
showing streams that are already live slowing while others replay.

## Implementation issues

Ordered; each about one pull request with its own tests, `high` risk
(`internal/realtime/**`). None starts before the maintainer decides to
build the reader ([Deciding](shared-reader.md#deciding-whether-to-build-it)).
Production changes only with the fourth; until then a stream without
readers runs today's path, which also stays selectable for the benchmark.

1. **The reader and its window:** registry, lifecycle, loop, eviction,
   failure and shutdown; no stream uses it yet. Tier A.
2. **Fan-out to caught-up streams:** a stream inside the window reads from
   it and waits on the reader; a mixed-language test; #209's rules on that
   path; a slow stream blocks nothing. Tier A.
3. **The hand-over:** the per-read choice between window and database and
   the wait on the reader only, with deterministic gap and duplicate tests.
   Tier A.
4. **Wiring:** `cmd/ribbitto` builds the registry and waits for it at
   shutdown, and streams use it. Tier A.
5. **Documentation and measurement:** the design moves out of *planned*
   into [streaming](streaming.md), [replay](replay.md),
   [real time](realtime.md), [stream limits](stream-limits.md) and the
   package documentation; `TestStreamCost` runs on the reader and records
   its results, with the distinct-members case, in
   [stream cost](stream-cost.md) next to #227's. Tier B.

The issue numbers are added here once they are filed.
