# Load client: restarting a child server

How [the load client](load-client.md) restarts a server it started itself, for
#219's reconnect storm. The reconnect model, receipts and the run-file format
are in the load client's page.

`-server PATH` starts an owned child; repeat `-server-arg ARG` for arguments.
It inherits the environment and overrides `RIBBITTO_ADDR` with `-server-addr`
(default `127.0.0.1:8080`, loopback IP and fixed port). Every launch refuses
occupied addresses. Direct readiness requires the first
token's channel page to answer 200 with an `events?after=` cursor while the child
is running; `-target` may point to Caddy. Target readiness is separate.
`-start-deadline` bounds each check (default 30s, >0 through 5m).

`-restart-after D` requires `-server`, enables reconnects and counts from setup's
end (default 0 disables; positive values must be below `-duration`). Streams
must establish during setup and stay open at the trigger; otherwise
`Restart.IncompleteSetup` is true and no storm runs. SIGTERM-to-exit takes
at most `-exit-deadline` (default 10s, >0 through 5m), then the child is killed and
reaped. `Restart.ExitSeconds`, `ExitOverrun` and `ReadySeconds` report exit time,
deadline overrun and SIGTERM-to-target readiness. `Error` reports a failed storm.
Posts keep their fixed schedule and retry model through the restart. Cleanup
stops and reaps the owned child, including on interruption.

After POST attempts/retries settle, the page supplies the final watermark.
Drain completes when every stream has no reset, re-establishes
after SIGTERM and its retained completed-event state reaches that watermark.
If watermark equals initial cursor, re-establishment suffices. Already holding
the watermark before SIGTERM still requires reconnecting; otherwise the full
`-drain` runs. Receipts freeze with the watermark in their header.
Expected sets read their own watermark through `Reader.Page`; the comparison
reports a mismatch (`watermark_differs`). In-memory hand-over to #630 retains `SIGTERM`, the new child's
`ReadyAt` and `RecoveryCursor`, each stream's `EstablishedAt`, and each completed
event's `ArrivedAt`. Times are not serialized.
With `-metrics`, `Restart.Old` is read just before SIGTERM and `Restart.New` after
drain; separate process snapshots have no delta.
