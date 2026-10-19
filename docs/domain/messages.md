# Messages

Message identity, accepted bodies and rendering. Read before changes;
update in the same pull request when rules change.

## Identity and format

A message is identified by its `id` (UUIDv7) — in the DOM, pin metadata and
its URL, and later in reactions, editing and deleting. `event_seq` orders messages
and drives paging and unread counts (unique per organisation). An event
reads its message by organisation, channel and `event_seq`; the message's
stable identifier for the DOM and links remains `id`.

A composer send may store a random UUID **idempotency key**
([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md#b-idempotency-key-1155)).
Unique per organisation/author, it grants nothing, is hidden from others
and lives with its message. Keyed posts store `typed_body_proof`: the
normalized typed body's 4-byte big-endian byte length and SHA-256 (36 bytes).
No backfill: legacy proofs stay NULL; unkeyed posts and notices have neither.

Bodies store a `format`; the stored body is the source of truth.
Stored messages are never reinterpreted; rendered HTML is only a disposable cache
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

Every channel message is in exactly one topic of its channel
([`topics.md`](topics.md)). Branching moves it to another topic without
changing its `id` or `event_seq`.

A reply will remain an ordinary message in the stream, linked to the
message it answers; the link opens a paged reply-chain view
([`replies.md`](replies.md), *planned*).

A message may quote one earlier message of the same channel
([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md)).
Whole and editable part quotes are implemented; page selection is planned (#1161).
The quote stores the original's ID, author member ID and text snapshot.
In the posting transaction, absent `quote_text` copies the whole extraction;
a present field, even empty, means part. `ValidateQuotePart` normalises with
the body rules and requires a substring of `Entry.Text`; invalid parts get 422.
The snapshot belongs to the quoting message and survives edits, moves and
deletion of the original; deletion nulls only its ID. A quote is not a reply.

Channel pins reference the current message; permissions, limits and lifecycle:
[`pins.md`](pins.md) (implemented in #887–#890 and #1205).

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

So `hello\t` becomes `hello`, but a forbidden character fails before
trimming. Everything else is kept as typed: no NFC normalisation, and
format characters such as the zero-width joiner in emoji sequences survive.

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

The channel page shows the latest page of 50 messages, oldest first. templ escapes
stored bodies at render time (never `templ.Raw`); `whitespace-pre-wrap`
keeps line breaks and `dir="auto"` isolates each body's direction. DOM ids
use `message.id`. Authors use `view.MemberName` with current display names
and handles. Times have a UTC `<time datetime>` fallback; an external script
shows browser-local time. A quote, above the body and outside its `body-` id, shows its author's current name, the
snapshot escaped the same way and a link built from the slug and message ID
([decision 36](../decisions/36-one-message-has-a-stable-url.md)), or no link
once the original is deleted; nothing current from the original, so it
renders the same for every reader.

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

Enter sends by default, or Ctrl/⌘+Enter by account choice; Shift+Enter adds a newline. IME never sends.
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
