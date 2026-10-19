# 38. A quote is a snapshot owned by the quoting message; text extraction is per format

**Decided** (#1153, approved by the maintainer). Text extraction and two of
its users (the pins excerpt and the live announcement) exist (#1204), as do
storage, posting and display of whole quotes (#1176), with the Quote action and
composer chip (#1160), and editable parts (#1161). Page text selection
remains planned (#1161).
The body format, its rendering and how a pasted message URL renders belong to
#1171 ([decision 39](39-message-bodies-use-a-small-frozen-markup.md):
rendering and extraction exist), which adds its format's extraction rule under the contract below.

## What a quote stores

A message may carry one quote, in three nullable columns on `message`: the
quoted message's ID, its author's member ID and a text snapshot. The member
reference is organisation-scoped like `message.member_id`, so the author's
current name still shows after the original is deleted. The snapshot is
under the same checks as a body (1–4000 code points, the rejected
characters, no surrounding white space); the quoting message keeps its own
body of 1–4000 code points, and the existing checks are unchanged.

A quote copies only the original's own text, never the original's quote
(one level). For a whole message the server copies the text itself; the
client sends only the message ID, never the text. For a selection (#1161)
the client sends the selected text, and the server accepts it only if it
appears in the original's extracted text (below), so nobody can attribute
invented text to another member. With `plain_text` and
`whitespace-pre-wrap`, the rendered text equals the stored text.

## The reference and its constraint

One policy, settled now rather than by the deletion feature. Author and
snapshot are both present or both null, and the message ID is present only
with them. The permitted states are:

- all three null: an ordinary message;
- all three present: a quote;
- author and snapshot present, message ID null: a quote whose original was
  deleted.

A foreign key on `(organization_id, quoted_message_id)` references
`message (organization_id, id)` with `ON DELETE SET NULL (quoted_message_id)`
(a column list, PostgreSQL 15 and later; the project runs 18), so
[invariant 2](../domain/invariants.md)'s composite keys hold and the database
keeps the reference honest. The quoted member references
`member (organization_id, id)`. There is no foreign key on the original's
channel. Quotes never block deleting or moving the original; the author and
the snapshot always stay together.

## Posting checks

Posting checks the quote in the posting transaction, under the organisation-first
lock ([posting](../architecture/posting.md)), before the insert:

1. the original is read scoped to the membership's organisation **and** the
   posting channel, so the poster may read it and it is in the same
   organisation and channel as the new message;
2. for a selection, the sent text appears in the original's extracted text.

That lock is the one pin writes take to serialise with moves
([pins](../domain/pins.md)), so a concurrent move cannot slip between the
check and the insert. A check that fails stores nothing and consumes no
`event_seq`. An unknown original, another organisation's, another channel's
and an unreadable one answer the same, and the answer contains none of the
original's text. The scoped read fails before any text is compared, so the
selection check is no oracle for other channels.

## What a quote shows later

The snapshot is an independently retained copy that belongs to the quoting
message: anyone who may read the quoting message's channel sees it.
Visibility of the snapshot is separate from access to the original. The
guarantee is scoped to posting time: a quote copies only text that every
reader of the destination channel could already read in that channel. Edits,
a move to another channel, deletion and a later loss of access to the
original leave the snapshot as posted.

Only the link follows
[decision 36](36-one-message-has-a-stable-url.md). It is built from the slug
and the message ID alone, resolves to the original's current place when the
reader may read it, and answers 404 when the original is deleted or the
reader may not read where it is now. A quote whose message ID is null shows
no link. The author is shown with the current name through org's member
directory, as `view.MemberName` is today. A quote shows nothing current from
the original (body, channel, topic), so it renders the same for every reader
and the shared render cache (`internal/web/stream_renderer.go`) stays valid.

A quote is not a reply. [Decision 22](22-replies-stay-in-the-stream-with-a-reply-chain-panel.md)
rejected copied text as the way to answer a message; a quote is a deliberate
copy, chosen by the person quoting, and replies stay as decided there.

## Text extraction

Each body format has one extraction function, owned by `conversation`, that
returns a message's plain text. For `plain_text` it returns the body
unchanged. It is used for:

- the pins excerpt ([pins](../domain/pins.md), 200 code points; shortening
  and collapsing white space stay with the excerpt);
- the live announcement (`view.LiveMessageItem`'s `data-announcement`,
  since #1204);
- checking a selection;
- later, notifications and search.

A quote's snapshot is left out of the quoting message's extraction, so quoted
text never adds a mention notification, a search hit or announcement text.
#1171 adds the extraction rule for its new format under this contract
([decision 39](39-message-bodies-use-a-small-frozen-markup.md)),
without reinterpreting stored bodies
([decision 18](18-stored-message-formats-never-change-meaning.md)).

**Why:** a quote must keep what the quoting member chose to answer, as email
quoting does, without letting anyone attribute invented text to another
member or copy text that readers of the destination channel could not read.
Checking at posting time, under the lock moves already take, makes that
guarantee hold without a later write to quoting messages. One extraction
function per format gives every feature that needs plain text the same
answer.

**Considered:**

- All three columns present or all null, with no foreign key on the message
  ID (needs an amendment to invariant 2; after deletion the link dangles and
  answers 404 instead of the quote saying it has no original).
- Redacting the snapshot when the original is deleted or moves to a channel
  some readers cannot read: stricter, but it needs a write to every quoting
  message on each move or delete, and it differs from email-style quoting,
  where the quoter's message keeps what it quoted.
- Showing the original's current text, as a pin does: it would diverge per
  reader after a move, and the shared render cache would no longer hold.
- A foreign key on the original's channel: a cross-channel move would have to
  rewrite or be blocked by every quote.
- Text sent by the client for a whole message: nothing to check it against
  that the server could not copy itself.
