# Web state and swap zones

## Progress

`web/src/stream-progress.ts`: unwired (A4b, #2067).
Page: reconnect/bfcache; Uc/Rc reset per source.
Separate: sidebar (S,u), pins seq, ephemeral generations.

|Value|Meaning|
|---|---|
|P0|Snapshot; initial Ap/Sh/G|
|Ap|Max applied/no-op|
|Sh|Visible Ap; showing catches up after reset too|
|G|Max applied/no-op/checkpoint (after)|
|F|First failure {reason,id:decimal/null}|
|Uc|after ≤ G|
|Rc|Last durable decimal id; unset initially|

BigInt: exact >2^53. P0 ≤ Sh ≤ Ap ≤ G; monotonic.
Durable: message/messages-moved/checkpoint/reset. Ignore other kinds' inherited lastEventId, invalid raw ids and gaps.
Applied/no-op advances Ap/G (equal allowed), visible Sh; checkpoint: only G.
Apply/decode: F only; history-reapply retains receipt's Ap/G.
Compatible proceeds past F; strict stops Ap/G even for null.
Terminal reset freezes Ap/G for any/no id; higher permits Rc > G.

**Safety S: resume X ≤ G.** New/no Rc: Uc; else native: Rc. Rc ≤ G: failure-free before reset, after synchronous outcomes.
Lower X: replay/retention reset. Strict wiring needs A-F approval: close during
failure, reload guard; model cannot ensure S.

## State classes

|Class|State; owner; end|
|---|---|
|S server|Messages/membership/read ranges/send-key preference; server|
|P projection|Message DOM/badges/pins seq; zone owner; reconcile/reset|
|U transient|Text/selection/IME/picker token/scroll anchor; controller; removal/page end|
|O operation|Queue/send/unconfirmed+restored keys/Remove hold/read retry/resync; owner; outcome/reload (draft survives), holds: pagehide|
|C connection|Cursors/source/status; Progress/stream; reset|
|D device|Drafts/settings; drafts/panel; reload-safe; sign-out/expiry/7d/eviction|

## Swap zones

Never replace #message-{body,pending,composer}/#organization-stream.
U/O survives swaps except own outcome.
Primary rebuilds; secondary: own display.
Add zones. m=#message-; owners: c composer, a conversation applier, u unread, p pins.

|Zone|Replacement; owner; secondary|
|---|---|
|m-items children|message/move; a; Delivery, branch, pins, enhancers, unread divider, read|
|m-items head, #load-older|Load older; a; enhancers|
|#sidebar-channels|sidebar/resync OOB #stream-sink; u; drafts, arrival, scroll|
|#topic-heading-count|Same OOB; u|
|#presence-<id>|presence OOB; none|
|#typing-status|typing; none|
|#pins-section|pins/pin response OOB; p; focus|
|Pin forms|pins-sync.ts htmx.ajax; p|
|m-composer-state|POST/pending; c; drafts, post-scroll|
|Typing form|Same POST OOB; none|
|#composer-quote|quote/composer POST OOB; c; growth|
|#mention-listbox|#mention-search; picker|
|#branch-feedback|branch POST; none|
