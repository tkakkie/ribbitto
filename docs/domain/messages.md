# Messages

What a message is, which bodies are accepted and how bodies are shown. Read
this before changing message validation, storage or rendering.

**Keep it current:** update this file in the same pull request whenever
these rules change.

## Identity and format

A message is identified by its `id` (UUIDv7) — in the DOM, pin metadata and
its URL, and later in reactions, editing and deleting. `event_seq` orders messages
and drives paging and unread counts (unique per organisation). An event
reads its message by organisation, channel and `event_seq`; the message's
stable identifier for the DOM and links remains `id`.

A message posted from a composer may also store that composer's
**idempotency key**, a random UUID the server renders into the form
([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md#b-idempotency-key-1155)).
An organisation and author have at most one message per key; another member
or organisation may use the same key for their own message. The key grants
nothing, is never shown to anyone else and lives as long as its message.
Branching's notices and messages posted without a key have none.

Bodies are **plain text** today, and the stored body is the source of truth.
A stored message is never reinterpreted, and rendered HTML is at most a
disposable cache
([decision 18](../decisions/18-stored-message-formats-never-change-meaning.md)).
*Planned* ([decision 39](../decisions/39-message-bodies-use-a-small-frozen-markup.md)): a `format` column arrives with `chat_v1`,
a small markup frozen per version (code spans and fences, block quotes,
bold, italic, `http`/`https` autolinks and member references `<@uuid>`);
every existing message stays `plain_text`, and new posts use `chat_v1`.
A reference to a member who is not in the URL's organisation, checked
through org's directory, rejects the post with 422.

Every channel message is in exactly one topic of its channel
([`topics.md`](topics.md)). Branching moves it to another topic without
changing its `id` or `event_seq`.

A reply will remain an ordinary message in the stream, linked to the
message it answers; the link opens a paged reply-chain view
([`replies.md`](replies.md), *planned*).

A message may quote one earlier message of the same channel
([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md), *planned*: #1176, #1160, #1161). The quote stores the
quoted message's ID, its author's member ID and a text snapshot that the
server copies or, for a selection, checks against the original's extracted
text, in the posting transaction. The snapshot belongs to the quoting
message and stays as posted when the original is edited, moved or deleted;
deletion sets only the quoted message ID to null. A quote is not a reply.

Channel pins reference the current message; their permissions, limits
and lifecycle rules are in [`pins.md`](pins.md) (implemented in #887–#890; live delivery planned).

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

The database checks the length, the absence of CR, the rejected controls,
separators and bidi characters (listed one by one, not as a range), and
that the body does not start or end with ASCII white space; PostgreSQL
`text` rejects NUL. The Unicode-aware trim stays in conversation.

## Showing bodies

The channel page shows the latest page of 50 messages, oldest first. templ escapes
stored bodies at render time (never `templ.Raw`); `whitespace-pre-wrap`
keeps line breaks and `dir="auto"` isolates each body's direction. DOM ids
use `message.id`. Authors use `view.MemberName` with current display names
and handles. Times have a UTC `<time datetime>` fallback; an external script
shows browser-local time. A quote (*planned*, #1176) shows its author's current name, the
snapshot escaped the same way and a link built from the slug and message ID
([decision 36](../decisions/36-one-message-has-a-stable-url.md)), or no link
once the original is deleted; nothing current from the original, so it
renders the same for every reader.

**Plain text from a message** comes from one extraction function per format,
owned by `conversation` ([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md), *planned*); for `plain_text`
it returns the body unchanged. The pins excerpt, the live announcement
(today `view.LiveMessageItem` concatenates `entry.Body`), the selection
check and later notifications and search use it. A quote's snapshot is left
out of the quoting message's extraction.

A `chat_v1` body (*planned*, [decision 39](../decisions/39-message-bodies-use-a-small-frozen-markup.md)) is parsed by
`conversation` into plain structs and rendered node by node with templ,
each block and inline element isolated in direction; never `templ.Raw` or
HTML built from strings, no raw HTML or images. Links allow only `http` and
`https`, with `rel="noopener noreferrer"`, and are never fetched; a pasted
message URL is an ordinary link. A member reference shows the current
`Display name @handle`, or a localised placeholder when it cannot be
resolved. Its extraction resolves references to `@handle` at use.

## Posting from the channel page

The bottom composer posts through `conversation.Posting.Send`. On the
latest page, htmx replaces only the composer with an empty form; the message arrives
through the stream (on reconnect if disconnected). A 422 replaces only the
composer, keeping the draft and its field error. Older pages use a plain form:
success gets a 303 to the latest page; errors render a full latest page with
the draft. Posting without JavaScript uses the same 303/422 flow. The channel
and organisation are resolved before posting; a non-member gets 404.
Enter sends, Shift+Enter inserts a line break, and IME composition never
sends. The page opens at the newest message; live delivery keeps it in view only
when the reader is already at the bottom, except for their own successful post:
the composer's success fragment identifies its message, and the sender scrolls
to that item even when scrolled up. This works whether the stream arrives before
or after the response, including delivery after reconnect. Each successful
post's scroll intent is consumed once; failed posts create none. Focus stays in
the composer. Live appends are announced politely;
replay duplicates replace the same message id without another announcement.

A **repeated send** (the same key again, after a lost response or a double
submission) posts nothing: it is answered as a success naming the first
message, wherever branching has moved it since, and the composer stays on the
requested feed or topic. The same key with other text or another channel is
not a retry: 409 keeps the text with an error and a fresh key, so sending
again posts it. A malformed key answers 400; a form without one posts without
deduplication.

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
