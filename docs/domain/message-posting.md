# Message posting

Posting rules for [messages](messages.md) from the channel page.

## Posting from the channel page

Latest feeds/topics empty the composer into pending items (#1156, #1188, #1174,
#1180). All states count toward 10 slots per tab; overflow keeps text with a notice.
Delivery, move-out, Remove/Edit free slots; Retry reuses its slot.
FIFO requests run one at a time and never overlap ordinary sends.
Not sent retries go last; unknown outcomes pause until same-key Retry succeeds,
Remove or Edit. Remove/Edit allow late commits out of order; uninterrupted
success and same-key Retry preserve storage order.
Edit restores text/key and uncertainty to an empty composer outside IME, focuses
its end and removes the item; otherwise it is disabled with a description.
State swaps and refusals retain the key and uncertainty; emptying renews the
key and clears uncertainty. Pending responses keep text; failures never swap it.
Idle blank/over-limit sends, or without
`crypto.randomUUID()`, use the ordinary path: success removes the sent prefix
(#1173); 422 keeps text and a field error. The textarea stays usable.
The stream delivers real items and replay. Older/scriptless posts
use 303 on success and full-page 422 errors. Organisation and channel resolve
before posting; non-members get 404.

[Device drafts](../architecture/web-composer.md#drafts): session-scoped feed/topic
restore; confirmation keeps newer text; Retry never snapshots.
Text and key are saved at dispatch, including in-flight reload/navigation (#1328).
Restore reuses the saved key only while text is unchanged; editing or emptying
renews it. A 200 or stored-key confirmation clears the saved key.
See the [Remove hold](../architecture/web-composer-pending.md#remove-hold) for its lifetime
and reload limit.

[Send-key modes](../architecture/identity.md) define shortcuts; IME never sends.
Pages start at newest; live delivery follows only at the bottom. The sender's
response ID scrolls once in either delivery order and after reconnect. Focus
stays in the composer. Live appends announce; replay replaces IDs silently.

**Repeated sends** compare whole normalized typed bodies by proof. Legacy
NULL-proof rows require exact stored text without references (`plain_text`,
or `chat_v1` with none). A prefix is not a retry. Unused keys post normally.
Equal retries return the stored message without writing, even after a move
or handle change; the composer stays on the requested feed/topic. Different typed text, quote or
channel gets 409 and a fresh key for Retry; pending 409 shows `message.error.repeat`.
Malformed keys get 400; absent keys disable deduplication. Enhanced failures
(#1179) keep text/key: 400/404/413/422/429 mean not sent unless an earlier send is
unconfirmed; network errors, 30 s timeouts, other 4xx except 409 and 5xx are
unconfirmed. Retries cannot post twice. Ordinary 409 recovers only a proven typed
prefix under the [composer conditions](../architecture/web-composer.md#composer).

One message has its own URL ([decision 36](../decisions/36-one-message-has-a-stable-url.md), #1170),
`/organizations/{slug}/messages/{message-id}`. It answers 303 to the message's
current topic, scrolled to the message and highlighted instead of the newest:
the latest page when the message is among the topic's newest 50, otherwise an
older `?around=` page that holds the message with up to 25 messages from it on
and older ones before it. Unknown, other-organisation and unreadable messages
answer 404.
