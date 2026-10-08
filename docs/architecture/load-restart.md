# Load client: restarting a child server

How [the load client](load-client.md) restarts a server it started itself, for
#219's reconnect storm; its results are in
[reconnect storm results](load-results-restart.md). The reconnect model,
receipts and the run-file format are in the load client's page.

`-server PATH` starts an owned child; repeat `-server-arg ARG` for arguments.
It inherits the environment and overrides `RIBBITTO_ADDR` with `-server-addr`
(default `127.0.0.1:8080`, loopback IP and fixed port). Every launch refuses
occupied addresses. The check binds and closes the port, so another local
process could claim it before the child does and receive the readiness probe's
seeded test-account cookie: load runs use a disposable, loopback-only machine,
and a hostile local process is outside the threat model. Child stderr goes to the caller's stderr. Direct readiness
requires the first token's channel page to answer 200 with an `events?after=`
cursor while the child is running; `-target` may point to Caddy. Target readiness
is separate.
`-server-addr`, `-server-arg`, `-start-deadline` and `-exit-deadline` require
`-server`. `-start-deadline` bounds each check (default 30s, >0 through 5m).

`-restart-after D` requires `-server`, enables reconnects and counts from setup's
end (default 0 disables; positive values must be below `-duration`). Streams
must establish during setup and stay open at the trigger; otherwise
`Restart.IncompleteSetup` is true and no storm runs. SIGTERM-to-exit takes
at most `-exit-deadline` (default 10s, >0 through 5m), then the child is killed and
reaped. `Restart.ExitSeconds`, `ExitOverrun` and `ReadySeconds` report exit time,
deadline overrun and SIGTERM-to-target readiness. `Error` reports a failed storm;
a failed relaunch returns its cause before attempting a watermark read.
A child that exited before SIGTERM is reported as an error.
Posts keep their fixed schedule and retry model through the restart. Cleanup
stops and reaps the owned child, including on interruption.

After POST attempts/retries settle, the page supplies the final watermark.
Streams with a reset are excluded. Drain completes when every other stream
re-establishes after SIGTERM and its retained completed-event state reaches
that watermark. If watermark equals initial cursor, re-establishment suffices.
Already holding the watermark before SIGTERM still requires reconnecting; otherwise the full
`-drain` runs. Incomplete setup uses the ordinary delivery drain predicate.
Receipts freeze with the watermark in their header.
Expected sets read their own watermark through `Reader.Page`; the comparison
reports a mismatch (`watermark_differs`). Only `-restart-after` runs record the
in-memory recovery state: `SIGTERM`, the new child's
`ReadyAt` and `RecoveryCursor`, each stream's `EstablishedAt`, and each completed
event's `ArrivedAt`. Stream `EstablishedAt` and `ArrivedAt` stay in memory;
`SIGTERM` and `ReadyAt` are serialized in the result.
With `-metrics`, `Restart.Old` is read just before SIGTERM and `Restart.New` after
drain; separate process snapshots have no delta.

## Client recovery and replay load

After drain, `Restart.Recovery` reports `Reconnected`, `OutageRecovered` and
`FullyCaughtUp`, plus `LiveAgain` for positive-rate runs, each with
`P50Seconds`, `P95Seconds`, `MaxSeconds` from SIGTERM and `IncompleteStreams`.
Streams that received a reset are excluded from all four times, both their samples and their incomplete-count denominators, and
from both replay counts; `ResetStreams` reports their number separately.
Quantiles use the nearest rank over completed streams; zero samples give zero
times. A positive incomplete count means a non-reset stream was still short
when the recovery deadline or drain ended, including streams that never
re-established. Incomplete setup
omits `Recovery` because no restart ran. These measurements are separate from
the server's exit time and the ordinary workload verdict.

Reconnected is the first successful stream re-establishment after SIGTERM.
Outage recovered is `max(reconnectedAt, cursorSatisfiedAt)`, using the new
child's page `RecoveryCursor` at readiness; fully caught up uses the final
watermark instead. `FullyCaughtUp` measures reaching the final watermark;
when posting continues until observation ends, the posting schedule dominates
this time. Satisfaction is the earliest completed event at or above the
target in the retained receipt state, including receipts before SIGTERM.
An idle current stream or an empty expected interval counts at re-establishment.
A reset stream never counts, even if it re-established or held the target.
`-recover-deadline` requires `-restart-after` (default 30s, >0 through 5m), counts
from SIGTERM and caps outage recovery; drain completion also caps it. Full
catch-up and reconnect delays are bounded by drain. Expiring the recovery
deadline does not shorten the drain, so later full catch-up is still measured.

Live again is the first arrival after the stream's first re-establishment of
an event whose POST was first attempted after that re-establishment. The marker
in the message receipt links the event to its POST's initial attempt time;
retries first attempted before re-establishment do not qualify, even if they
commit later. The cutoff is drain end, inclusive, independent of
`-recover-deadline`. A later arrival or no qualifying POST leaves the stream
incomplete, including a late reconnect after posting stops. Rate 0 reports
`LiveAgain: null` (N/A), rather than zero times or incomplete streams.
Replay on one connection is in sequence order, so this arrival proves receipt
through the qualifying post's sequence. It is a catch-up bound: it includes
waiting for the next qualifying POST and delivery; later commits may still
be queued. Its quantiles and incomplete count exclude reset streams as above.

`deliveries_through_recovery_cursor_after_reconnect` counts completed sequenced
events at or below the recovery cursor, received from the first re-establishment
through drain. `all_deliveries_after_reconnect` counts all such deliveries,
including those above the cursor; `Reconnected` gives the reconnect-delay
distribution. Both include repeated arrivals and deliveries across later
reconnects on non-reset streams; neither is the comparison's
`replayed_duplicates`. The bounded cursor count leaves out backlog committed
after readiness and before a delayed stream reconnects. #219's report must
repeat this limitation alongside the total and reconnect-delay distribution.
