# Stream resource limits

What bounds the cost of open Server-Sent Event streams. Ordering, replay
and authorization are in [`streaming.md`](streaming.md).

- Each cache (events, renders) runs at most 16 loaders at once, second
  loads included; a slot is held until its loader returns, even after a
  timeout. Waiting for a slot shares the 10 s load timeout, and a caller
  can leave sooner through its own context. The last waiter to leave cancels a load, the last joiner its
  second load, and shutdown every load (`cmd/ribbitto` passes the process
  context, through `Streaming` for renders).

- Each connection reads the log itself (#209), so nothing queues for a slow
  client: its cursor just lags. A client that stops reading is
  disconnected when a write, event or heartbeat, misses its deadline
  (`DefaultStreamWriteTimeout`, 10 s; #160). The server's `WriteTimeout` bounds ordinary responses and
  would cut a long-lived stream, so the SSE handler does not inherit it: it
  sets a finite deadline before every write with
  `http.ResponseController.SetWriteDeadline`, flushes, and clears the
  deadline before waiting (#158) — under HTTP/2 an expired deadline resets
  the stream even while idle. If it cannot flush, the stream ends. The
  server's read timeout does not cut it: net/http clears the read deadline
  once the request is read. `TestEventStream` idles past all of them.
- Middleware that wraps `http.ResponseWriter` implements `Unwrap` so
  flushing works; compression is not applied to the SSE endpoint.
- An idle stream writes an SSE comment (`: heartbeat`) once
  `DefaultStreamHeartbeat` (20 s; #160) has passed since its last write,
  however often another channel's events wake it and while it drains a
  backlog of them: proxies keep it open, and a client
  that stopped reading is found out at that write's deadline. The loop
  sends it while waiting on the hub (`Stream.Heartbeat`); a failed one ends
  the stream without moving the cursor. Presence (M4) will wait about 30 s
  after a disconnect before showing a member as offline, so a reload does
  not flicker.
- An account holds at most `DefaultMaxStreamsPerAccount` (16) streams,
  counted atomically by the hub's registration (#157, #207). One more is
  refused with 429 before it starts, rather than closing the oldest, which
  would make that tab reconnect and close the next, forever. The htmx SSE
  extension retries a refused stream with back-off, 0.5 s doubling to 64 s,
  and connects once a slot frees. The count is per process; there are no
  limits per address or installation-wide.
- On shutdown, the server ends every stream first (`RegisterOnShutdown`
  with `Hub.CancelAll`, cause `ErrShutdown`): an open stream never goes
  idle, so `Shutdown` would otherwise wait for it until its deadline. The
  hub then refuses every registration, so a request accepted just before
  gets 503 instead of a stream.
  Browsers reconnect to the next process and replay from their cursor.
  Production serves HTTP/2 through Caddy, because browsers allow only six
  HTTP/1.1 connections per origin.
