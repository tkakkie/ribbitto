# 31. Presence and typing are current state with a generation

**Decided** (#296; amends [decision 23](23-the-event-log-is-the-source-of-truth-the-hub-carries-a-level.md):
the hub carries a level per ephemeral kind besides the durable one; durable
events are unchanged). Hub generations and the combined writer wait are
implemented (#735), as are frame delivery (#736) and the sink (#737);
presence state, changes and rendering are current (#766–#768; closes #287); typing (#288) remains planned. Details in
[real time](../architecture/realtime.md#ephemeral-state).

- **Current state, not events.** Presence (who has a stream open) and
  typing (who is typing where) are in-memory state per organisation and per
  process, owned by the feature that shows them. A change that alters what
  a member would see raises that kind's **generation**, a per-organisation
  level; a refresh that changes nothing visible (another keystroke while
  already shown as typing) raises nothing. The owner publishes the state
  before raising its generation, and a read returns the generation the
  state it returns represents, so a connection never marks unseen state
  seen. The state is never written to `event_log`, queued per connection,
  fanned out as events or replayed.
- **The hub carries levels.** Per organisation: the committed sequence, as
  decision 23 says, and one generation per ephemeral kind. It still never
  holds or forwards events or state; a connection that wakes reads the
  current state from its owner.
- **One writer per stream** wakes when the durable level passes its cursor,
  when the generation of a kind it is interested in passes the one it has
  seen, or when its heartbeat is due. After each durable batch (at most
  100 events) and before waiting, it sends at most one coalesced frame per
  kind whose generation advanced: durable events keep their order, wait at
  most for those frames, and a durable backlog delays an ephemeral change
  by at most one batch.
- **No `id:`.** Ephemeral frames carry no SSE `id:` line, so neither
  `Last-Event-ID` nor the durable cursor moves.
- **A reconnect restores current state, within bounds.** Typing is sent
  whole (the indicator for the connection's channel or topic, naming at
  most three typists and counting the rest) at every connect and whenever
  it changes, read from a per-place summary in constant time. Presence sends only the entries changed after the token the
  page was rendered with (process instance and generation), at most a fixed
  number per frame. A token or connection the process cannot serve gets
  [decision 24](24-a-cursor-that-cannot-be-served-gets-reset.md)'s
  `reset`, and the page reloads with fresh state: another process's token,
  one below the boundary of discarded changes (a monotonic level raised
  whenever a kept change is dropped), or more changes than one frame
  holds.
- **Authorization.** Each frame is checked for its connection immediately
  before it is sent, by the rule for an organisation-wide durable event in
  the same place: presence as one of the organisation, typing as one of its
  channel. A connection makes at most one check per kind per durable batch
  or wake, served
  by the cached allow and the shared fresh epoch (#670), however many
  changes the level coalesced.

**Why:** presence and typing are worth only their latest value. A queue
per connection is what decision 23 rejected (memory grows with slow
readers), and logging them would make replay and retention carry noise. A
level per kind keeps one wait that cannot lose a wake-up, like the durable
level, and a reconnect restores by reading state instead of replaying
(maintainer's direction, 2026-10-02).

**Considered:** a dropping buffer per connection in the hub (the
per-connection queue decision 23 rejected); logging presence and typing as
durable events (replayed noise, retention churn, the organisation's lock on
every keystroke); sending the whole presence list on every change or
connect (its cost grows with online members times connections).
