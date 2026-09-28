# Messages

What a message is, which bodies are accepted and how bodies are shown. Read
this before changing message validation, storage or rendering.

**Keep it current:** update this file in the same pull request whenever
these rules change.

## Identity and format

A message is identified by its `id` (UUIDv7) — in the DOM, and later in
permalinks, reactions, editing and deleting. `event_seq` orders messages
and drives paging and unread counts (unique per organisation) but is never
used as an identifier.

Bodies are **plain text**, and the stored body is the source of truth. If
Markdown or rich text comes later, an explicit format is added with it and
every existing message stays `plain_text`; a stored message is never
reinterpreted, and rendered HTML is at most a disposable cache
([`DECISIONS.md`](../DECISIONS.md), 18).

## Accepted bodies

`domain.ValidateMessageBody`, in this order:

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
`text` rejects NUL. The Unicode-aware trim stays in the domain.

## Showing bodies

A body is escaped by templ at render time (never `templ.Raw`) with its line
breaks kept. Each body is shown with `dir="auto"` or inside `<bdi>`, so its
direction — including any isolates it contains — cannot leak into the
surrounding interface.
