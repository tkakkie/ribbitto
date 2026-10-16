# 39. Message bodies use a small, frozen markup with member references

**Decided** (#1171, approved by the maintainer). Nothing here is implemented
yet; one later issue adds the migration, parser, renderer and composer help
together. It follows [decision 18](18-stored-message-formats-never-change-meaning.md)
and extracts text under [decision 38](38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md)'s
contract.

## Format and storage

`message` gains `format` (text, not null, a CHECK listing the known values)
with the first format after `plain_text`; every existing row becomes
`plain_text` and stays so. The new format is `chat_v1`, and new posts use it
from the release that ships it. A format's grammar is frozen once released:
a change of meaning is a new identifier (`chat_v2`), never an edit of
`chat_v1`. A quote's snapshot stays plain text (decision 38).

## Grammar of `chat_v1`

The grammar reads the body `ValidateMessageBody` stores, after its
normalisation (CR and CRLF become LF, white space trimmed at both ends); it
changes nothing further.
Blocks are found line by line:

- **Fenced code:** a line that is exactly three backticks, optionally
  followed by a language tag of 1–20 ASCII letters, digits, `+`, `-`, `#`
  or `.`, up to the next line of exactly three backticks. Its content is
  literal. Without a closing line the opening line is ordinary text.
- **Block quote:** consecutive lines starting with `>`, one following space
  removed. It holds inline content only; a further `>` is literal (one level).
- **Text:** every other line. Line breaks, blank lines included, are kept as
  typed.

Inline, outside code, each line is read once from left to right. At each
position the first rule that applies wins, in this order:

1. a backslash before ASCII punctuation makes that character literal; any
   other backslash is literal;
2. a code span is text between single backticks on the same line; its
   content is literal; without a closing backtick the backtick is literal;
3. a member reference is `<@` + a canonical lowercase member UUID + `>`;
4. an autolink is `http://` or `https://` at the start or after white space
   or `(`, with a host, up to white space, `<`, `>`, `"` or a bidi isolate
   or mark (LRI, RLI, FSI, PDI, LRM, RLM, ALM); trailing `.,:;!?'*_` and an
   unbalanced `)` stay outside it;
5. `**` (bold) and `_` (italic) are delimiters.

A delimiter can open when the next character is not white space, and close
when the previous one is not. `_` also needs a word boundary: it opens only
after the start, white space or punctuation, and closes only before the end,
white space or punctuation, where letters, digits and marks of any script
are word characters. So `snake_case` and `日本_語_` stay literal, while
`これは**重要**です` is bold. Open delimiters form a stack: a delimiter
closes the top of the stack if it can close and matches it, and otherwise
opens if it can, unless two are already open, when it is literal. A closer
that matches a delimiter deeper in the stack (crossing) is literal, and
delimiters still open at the end of the line are literal.

Everything else is literal: headings, lists, tables, images, raw HTML,
`*single asterisks*` and `[text](url)`, whose text could hide its target.

## Parser and pinning

`conversation` owns a small parser for this grammar, written for it, that
returns plain structs (blocks, inline nodes, references). It adds no
dependency and runs in time linear in the body's length. Golden tests fix
input → tree per format version; fuzz tests check that parsing and
extraction never panic, respect the nesting limit, and that an unresolved
reference node yields the placeholder, never its UUID.
Rendered HTML stays at most a disposable cache.

## Text extraction

`chat_v1`'s extraction returns text segments and member-reference segments.
The caller resolves references through org when it uses the text, to
`@handle`, or to a fixed localised placeholder when the member cannot be
resolved, never the UUID. No name is stored, so renames never leave stale
text. Delimiters, escape backslashes, fences and language tags are dropped;
code content is kept literally; each block boundary becomes one line break;
line breaks inside a block stay; an autolink is its URL. The pins excerpt
counts its 200 code points after resolution.

A quote of a `chat_v1` message copies its extraction, resolved when the quote
is posted, then trimmed of white space at both ends to meet decision 38's
snapshot checks; if nothing is left (a body of only `>`), the quote is
rejected like an invalid body. The shared extraction itself keeps code
content exactly. #1161 sends a selection in the same resolved form (a
selected mention as `@handle`), trimmed the same way before the check.

| Stored | Extracted |
|---|---|
| `**Deploy** _today_` | `Deploy today` |
| `` use `a*b` here `` | `use a*b here` |
| `\*\*not bold\*\*` | `**not bold**` |
| `> quoted line`, line break, `reply` | `quoted line`, line break, `reply` |
| a fence tagged `go` holding `x := 1` | `x := 1` |
| `see https://example.com/a.` | `see https://example.com/a.` |
| `**see https://example.com/x**` | `see https://example.com/x` (bold link) |
| `**a _b** c_` | `**a b** c` (the crossing `**` is literal; `b** c` is italic) |
| `hi <@{member UUID}>` | `hi @tomoya`, resolved at use |

## Member references

A token holds the member's UUID only, never a handle or name.

- **At posting,** every reference is looked up in the URL's organisation in
  one batch through `org.Directory.LookupMembers`, which omits missing and
  foreign members; never a direct query. If any is missing, the post is
  rejected with 422 and keeps the draft, as validation errors do today, with
  one message for an unknown and another organisation's member, so the
  answer reveals nothing about other organisations.
- A **malformed** token is not a reference: it stays literal text.
- **At rendering,** a reference shows the current `Display name @handle`
  through `view.MemberName` (#117, [names](../domain/names.md)). A member
  who later leaves or cannot be resolved shows the localised placeholder.
  Lookups go through the same organisation-scoped directory, so nothing
  from another organisation can show even if a bad row exists.
- **Negative example** for the implementation's tests: a stored body
  naming another organisation's member UUID never shows that member's
  display name or handle on the page, in the stream or in extracted text.

Mentions (#117) are stored only as these references, so #117 lands with or
after `chat_v1`'s implementation.

## Rendering and security

`conversation` parses; handlers convert the structs to view models; templ in
`internal/web/view` renders each node. Ruled out: `templ.Raw` and HTML built
from strings, raw HTML in bodies, images, and `templ.SafeURL` around a user
URL. Links take only `http` and `https` (an allow-list, checked again when
rendering) and carry `rel="noopener noreferrer"`. The server never fetches a
URL from a message: no unfurling, no previews. A pasted message URL
([decision 36](36-one-message-has-a-stable-url.md)) is an ordinary link
with nothing from the target message; ribbitto has no configured public
origin to recognise it by. Every block and every inline element (bold,
italic, link, code span, reference) is isolated in direction with
`dir="auto"` or `<bdi>`; plain text runs take their block's isolation.

Output is the same for every reader of one language (only the unresolved
member placeholder is localised), so the shared render cache, keyed by
language, stays valid; highlighting one's own mentions happens in the browser from a data
attribute. Existing headers already help: `Referrer-Policy: same-origin`
and the CSP's `img-src 'self' data:`.

**Why:** code, links and mentions need structure that plain text cannot
hold, and decision 18 forbids reinterpreting old bodies. A small grammar
written for chat keeps typed text meaning what it shows, and freezing it
per version keeps stored meaning stable.

**Considered:**

- goldmark's AST without its HTML renderer: mature, but its CommonMark
  block rules (joined soft breaks, indented code, setext headings, HTML
  blocks, reference definitions) need disabling, and an upgrade could
  change a stored message's parse.
- Structured parts: typed nodes without a text grammar, but a document
  schema and composer serialisation instead of typed text.
- Plain text with display rules (autolinks only): no format column, but no
  code blocks or member references.
- Keeping a reference to an unknown or foreign member as literal text
  instead of a 422: a wrong mention would post silently as a UUID.
