# Messages

What a message is, which bodies are accepted and how bodies are shown. Read
this before changing message validation, storage or rendering.

**Keep it current:** update this file in the same pull request whenever
these rules change.

## Identity and format

A message is identified by its `id` (UUIDv7) — in the DOM, and later in
permalinks, reactions, editing and deleting. `event_seq` orders messages
and drives paging and unread counts (unique per organisation). An event
reads its message by organisation, channel and `event_seq`; the message's
stable identifier for the DOM and links remains `id`.

Bodies are **plain text**, and the stored body is the source of truth. If
Markdown or rich text comes later, an explicit format is added with it and
every existing message stays `plain_text`; a stored message is never
reinterpreted, and rendered HTML is at most a disposable cache
([decision 18](../decisions/18-stored-message-formats-never-change-meaning.md)).

Every channel message is in exactly one topic of its channel
([`topics.md`](topics.md)). Branching moves it to another topic without
changing its `id` or `event_seq`.

A reply will remain an ordinary message in the stream, linked to the
message it answers; the link opens a paged reply-chain view
([`replies.md`](replies.md), *planned*).

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
shows browser-local time.

## Posting from the channel page

The bottom composer posts through `conversation.Posting.PostToTopic`. On the
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

## Older pages

Older history is read a page of 50 at a time with `ListMessagesBefore`,
bounded by the oldest shown `event_seq` (`?before=` on the channel URL); no
`OFFSET` and no count query — one extra row says whether an older page
exists. The bound is only a number: the read is still scoped to the URL's
organisation and channel, so a value taken from another channel returns
this channel's older messages, never that channel's. "Load older messages"
prepends the page with htmx and keeps the reader's place; without
JavaScript it is a link to that page, which links back to the newest. The
control is gone once the oldest message is shown.
