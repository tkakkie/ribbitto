# 23. The event log is the source of truth; the hub carries a level

**Decided:** real-time delivery reads durable events only from `event_log`
(ephemeral signals such as presence and typing are not logged and are out of
this rule). The in-memory hub carries, per organisation, only the highest
committed sequence it has been told about, and wakes waiting connections when
it rises; it never holds or forwards events. Losing a raise, or restarting the process, can only
delay delivery, which the periodic watermark check bounds. In M3 each
connection runs its own read loop; that is the current design, not part of
this decision, and the planned shared reader per organisation (#232, #236)
may replace it under the same rule. Details:
[real time](../architecture/realtime.md#durable-event-log) and
[streaming](../architecture/streaming.md#ordering-and-replay).
**Why:** one durable source gives replay, reconnection and recovery from a
missed notification the same code path, and keeps the hub small enough to
reason about (M3, #156–#158, #237).
**Considered:** fanning events out from memory (lost on a crash or a missed
notification, and replay would need a second path); per-connection queues
(memory grows with slow readers).
