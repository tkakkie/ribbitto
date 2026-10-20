# Messages

Identity, accepted bodies and rendering. Update with rule changes.

## Identity and format

A message's `id` (UUIDv7) identifies it in the DOM, pin metadata, URLs and later
reactions, editing and deleting. Organisation-unique `event_seq` orders messages
and drives paging/unread counts. Events read by organisation/channel/`event_seq`;
DOM/links keep the stable `id`.

A composer send may store a random UUID **idempotency key**
([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md#b-idempotency-key-1155)).
Unique per organisation/author, it grants nothing, is hidden from others
and lives with its message. Keyed posts store `typed_body_proof`: the
normalized typed body's 4-byte big-endian byte length and SHA-256 (36 bytes).
No backfill: legacy proofs stay NULL; unkeyed posts and notices have neither.

Bodies store a `format` and are the source of truth, never reinterpreted;
rendered HTML is a disposable cache
([decision 18](../decisions/18-stored-message-formats-never-change-meaning.md)).
Messages stored before `chat_v1` remain `plain_text`; [decision 39](../decisions/39-message-bodies-use-a-small-frozen-markup.md) adds `chat_v1`,
a small markup frozen per version (code spans and fences, block quotes,
bold, italic, `http`/`https` autolinks and member references `<@uuid>`).
New posts use `chat_v1` (#1203). Every reference is checked through org's
directory in the URL's organisation: one to an unknown or another
organisation's member rejects the post with 422 and one message for both,
keeping the draft; a malformed token is text. Branching's notice is
`chat_v1` too, but server text, never checked: escaped, a topic named
`**Deploy**` or like a reference shows as typed.

Every message belongs to exactly one topic of its channel
([`topics.md`](topics.md)). Branching moves it to another topic without
changing its `id` or `event_seq`.

A reply remains an ordinary stream message linked to the answered message;
the link opens a paged reply-chain view
([`replies.md`](replies.md), *planned*).

A message may quote one earlier same-channel message
([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md)).
Selections extract text (`@handle`, newline/block); cut/unresolved
mentions or unknown markup hide the button.
Quotes store original ID, author member ID and text snapshot. In the post
transaction, absent `quote_text` copies the whole extraction; any present field,
even empty, is a part. `ValidateQuotePart` normalises by body rules and requires
an `Entry.Text` substring; otherwise 422. The quoting message owns the snapshot,
which survives edits/moves/deletion of the original; deletion nulls only its ID.
A quote is not a reply.

Channel pins reference current messages: [`pins.md`](pins.md) covers permissions,
limits and lifecycle (#887–#890, #1205).

## Accepted bodies

`conversation.ValidateMessageBody`, in this order:

1. reject invalid UTF-8;
2. turn CRLF and lone CR into LF;
3. reject every control character except LF and TAB (C0, DEL, C1,
   including NUL), U+2028 and U+2029, and the bidi embeddings and overrides
   LRE, RLE, PDF, LRO and RLO (U+202A–U+202E);
4. trim white space (`unicode.IsSpace`) at the start and end of the whole
   body, not of each line;
5. require 1–4000 code points.

`hello\t` becomes `hello`; forbidden characters fail before trimming.
Everything else stays as typed: no NFC normalisation; format characters such
as emoji sequences' zero-width joiners survive.

**Bidi controls.** The embeddings and overrides are rejected because they
can make stored text read differently from how it is stored — a spoofed URL,
for example — and Unicode discourages them in new text. The isolates LRI,
RLI, FSI and PDI (U+2066–U+2069) and the marks LRM, RLM and ALM (U+200E,
U+200F, U+061C) are **allowed on purpose**: they are the recommended way to
mix directions in plain text with Arabic or Hebrew.

The database checks length, no CR, rejected controls/separators/bidi characters
(listed individually, not as a range), and no leading/trailing ASCII white space.
PostgreSQL `text` rejects NUL; Unicode-aware trim stays in conversation.

## Showing bodies

Channel pages show the latest 50 messages, oldest first. templ escapes bodies
(never `templ.Raw`); `whitespace-pre-wrap` keeps line breaks; `dir="auto"` isolates
direction. DOM ids use `message.id`. `view.MemberName` uses current display names
and handles. Times fall back to UTC `<time datetime>`; an external script shows
browser-local time. Quotes above the body, outside its `body-` id, show the author's current name,
snapshot escaped the same way and a link built from the slug and message ID
([decision 36](../decisions/36-one-message-has-a-stable-url.md)), or no link
once the original is deleted. No current original content; identical for all readers.

**Plain text from a message** comes from one extraction function per format,
owned by `conversation` ([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md)):
`Entry.Text` returns a `plain_text` body unchanged and a `chat_v1` body's
resolved extraction (below). Its users are pins excerpts
(200 code points after resolution), live announcements, quote snapshots
(trimmed; empty rejected) and part checks; later, notifications and search.
A quote's snapshot is left out of the quoting message's extraction.

A `chat_v1` body ([decision 39](../decisions/39-message-bodies-use-a-small-frozen-markup.md)) is parsed by
`conversation` into plain structs and rendered node by node with templ,
each block and inline element isolated in direction; never `templ.Raw` or
HTML built from strings, no raw HTML or images. Links allow only `http` and
`https`, checked again when rendering, with `rel="noopener noreferrer"`, and are never fetched; a pasted
message URL is an ordinary link. A member reference shows the current
`Display name @handle` (`view.MemberName`), or a localised placeholder when
the organisation's directory, read in the authors' batches, does not return
it. The markup is the same for every reader of one language. Its extraction
(`conversation.ExtractChatV1`) is text and member-reference segments:
delimiters, escapes, fences and language tags dropped, code kept exactly,
an autolink as its URL, one line break per block boundary. A reference
resolves at use, in the reader's organisation-scoped directory batch, to
`@handle` or to the caller's localised placeholder (`view.UnknownMember`),
never its UUID; a UUID typed as text or in code stays. So the announcement,
kept in the shared stream render cache, is the same for every reader of one
language too.

## Posting from the channel page

Latest feeds/topics empty into pending items (#1156, #1188, #1174, #1180).
All states count toward 10 slots/tab; overflow keeps text with a notice.
Delivery, move-out, Remove/Edit free slots; Retry keeps its slot.
FIFO requests never overlap each other or ordinary sends.
Not sent retries go last; unknown outcomes pause until same-key Retry succeeds,
Remove or Edit. Remove/Edit allow late commits out of order; uninterrupted
success and same-key Retry preserve storage order.
Edit restores text/key/uncertainty into an empty composer outside IME, focuses
its end and removes the item; otherwise disabled with a description.
State swaps/refusals retain key/uncertainty; emptying renews/clears them.
Pending responses keep text; failures never swap it.
Remove holds unconfirmed raw text/key only in this feed/topic's page memory
for 5 minutes (#1328). Identical text before the limit reuses it; different text
takes a fresh key. Expiry/pagehide drops the hold. Remove then reload loses it;
a stored post is visible in the conversation.
Idle blank/over-limit sends, or without
`crypto.randomUUID()`, use the ordinary path: success removes the sent prefix
(#1173); 422 keeps text and a field error. The textarea stays usable.
The stream delivers real items/replay. Older/scriptless posts use 303 success
and full-page 422 errors. Organisation/channel resolve first; non-members get 404.

[Device drafts](../architecture/web-composer.md#drafts) save text/key before dispatch,
including in-flight reload/navigation. Session-scoped restore reuses the key only
for unchanged text; any edit/emptying renews it. Confirmation clears the saved
key, keeping newer text; Retry never snapshots. Several tabs may safely share a key.

[Send-key modes](../architecture/identity.md) define shortcuts; IME never sends.
Pages start at newest; live delivery follows only at the bottom. The sender's
response ID scrolls once in either delivery order and after reconnect. Focus
stays in the composer. Live appends announce; replay replaces IDs silently.

**Repeated sends** compare whole normalized typed bodies by proof. Legacy
NULL-proof rows require exact stored text without references (`plain_text`,
or `chat_v1` with none). A prefix is not a retry. Unused keys post normally.
Equal retries return the stored message without writes, even after a move
or handle change; composer stays on the requested feed/topic. Different text, quote or
channel gets 409 and a fresh key for Retry; pending 409 shows `message.error.repeat`.
Malformed keys: 400; absent keys disable deduplication. Enhanced failures
(#1179) keep text/key: 400/404/413/422/429 mean not sent unless already unconfirmed;
network errors, 30 s timeouts, other 4xx except 409 and 5xx mean unconfirmed.
Retries cannot post twice. Ordinary 409 recovers only a proven typed prefix under
the [composer conditions](../architecture/web-composer.md#composer).

One message has its own URL ([decision 36](../decisions/36-one-message-has-a-stable-url.md), #1170),
`/organizations/{slug}/messages/{message-id}`. It answers 303 to the message's
current topic, scrolled to the message and highlighted instead of the newest:
the latest page when the message is among the topic's newest 50, otherwise an
older `?around=` page that holds the message with up to 25 messages from it on
and older ones before it. Unknown, other-organisation and unreadable messages
answer 404.

## Older pages

Older history is read a page of 50 at a time with `ListMessagesBefore`,
bounded by the oldest shown `event_seq` (`?before=` on the channel URL); no
`OFFSET` and no count query — one extra row says whether an older page
exists. The bound is only a number: the read is still scoped to the URL's
organisation and channel, so a value taken from another channel returns
this channel's older messages, never that channel's. "Load older"
prepends the page with htmx and keeps the reader's place; without
JavaScript it is a link to that page, which links back to the newest. The
control is gone once the oldest message is shown.
An `?around=` page reads up to 25 messages from its position on with
`ListMessagesAfter` and fills the page with older ones from
`ListMessagesBefore`, bounded and scoped the same way, so an `around` value
taken from another channel or topic never shows that channel's messages.
