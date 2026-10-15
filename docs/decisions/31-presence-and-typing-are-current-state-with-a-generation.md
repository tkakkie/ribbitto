# 31. Presence and typing are current state with a generation

**Decided** (#296; amends [decision 23](23-the-event-log-is-the-source-of-truth-the-hub-carries-a-level.md):
the hub carries a level per ephemeral kind besides the durable one; durable
events are unchanged). Hub generations and the combined writer wait are
implemented (#735), as are frame delivery (#736) and the sink (#737);
presence state, changes and rendering are current (#766–#768; closes #287); typing state, composer signals, delivery and page status regions are implemented (#784–#790, #813, #833; closes #288). Details in
[ephemeral state](../architecture/ephemeral-state.md#ephemeral-state).
Member read invalidation extends this contract (#711, option B): registry and
sidebar delivery are wired in production; core HTTP acceptance is implemented.
The three stacked #711 PRs cover production wiring/core HTTP acceptance,
race/isolation/recovery/browser acceptance, and short measurements/documentation.
[Results and evidence](../architecture/read-advance-results.md) record condition 5;
#832's longer comparison follows separately.

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
- **One writer per stream** wakes when the durable level passes its covered level,
  when the generation of a kind it is interested in passes the one it has
  seen, or when its heartbeat is due. After each durable batch (at most
  100 events) or generation wake and before waiting, it sends at most one
  coalesced frame per kind whose generation advanced: durable events keep their order, wait at
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

## Member read invalidation

Read state stays persisted; `unread.Changes` owns a process-local generation and
channel per active organisation/member, separate from the hub's kind levels.
Changes publish only after commit, allocate nothing without a subscriber and
wake only that member's waiting tabs. Last release removes the entry; generations
are comparable only within one active subscription lifetime.

Through optional interfaces beside `SidebarSource`, the single writer acquires
its watch before the initial recount and immediately defers release on every exit,
including errors and panics. It captures the generation before each fresh
persisted snapshot, then acknowledges only that captured generation after send
or explicit denial. Publication during recount remains pending for a later one.
Observed and covered generations stay separate: observing while throttled avoids
spinning without claiming coverage. Atomic generation/channel capture in the
combined wait covers publication before or after entering the wait.

Read changes dirty the existing sidebar coalescer, adding no loop or queue and
never moving its fixed deadline. Recounts run between batches; backlog, heartbeat,
cancellation and durable-cursor rules still apply. Rendering precedes authorization
with both `OrganizationID` and `AudienceMemberID` bound to the subscription's
member, rejecting revocation or replacement membership; frames carry no `id:`.

Notification costs one locked lookup/update and replacement channel per active
publication; wakes scale with matching waiting tabs. Memory follows active member
entries and watches. Recounts stay per connection and interval-limited; wakes
do not share that rate limit. After catch-up, member-watch-only wakes do not
read the event log unless a newer durable level requires it (#1106).

Healthy published changes converge to fresh counts; reconnect repairs omissions
by recounting persisted state. The registry is single-process. Multiple processes
need shared member invalidation and subscription/reconnect coordination; bounded
missed-notification recovery additionally needs durable revisions or replay.
