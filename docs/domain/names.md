# Names

How ribbitto names members: display names, handles and member ids. Read
this before showing a member's name, adding mentions, or changing the rules
for either name.

**Keep it current:** update this file in the same pull request whenever the
display-name or handle rules, or how names are shown, change. The general
validation rules (UTF-8, controls, NFC, printable characters) are in
[`validation.md`](validation.md#validation).

## Three jobs, three things

| | Stored on | Unique | Used for |
|---|---|---|---|
| **display name** | `account` | no — two members may share one | showing people who wrote something |
| **handle** | `member` | per organisation, ignoring case | helping people tell members apart |
| **`member_id`** | `member` | yes | everything the system decides or stores |

A display name and a handle are **for people only**. Permissions, mentions
and every stored reference use `member_id`, never a name, so either name can
change without breaking anything. Handles never sign in and are never
derived from the email address. A member is shown as `Display name @handle`
(for example `加藤 智也 @tomoya`); the handle, not a validation error, is
what tells two members with the same display name apart. Unicode
confusables in general are not detected or banned.

## Display names

1–50 characters after the rules in [`validation.md`](validation.md#validation), and
**not blank-looking**. A name is blank-looking when every character is one
of:

- the ASCII space U+0020 or the ideographic space U+3000;
- the Hangul fillers U+3164, U+FFA0, U+115F and U+1160;
- the braille blank U+2800;
- a combining mark (Unicode category M), which has no base character to
  attach to when nothing else is there.

The rule (`identity.IsBlankLookingName`) is this fixed list of code points and
one category — not a judgement of how a font or browser renders the name.
It does not promise to catch every string that some font shows as nothing.

| Name | Result | Why |
|---|---|---|
| `ㅤ` (U+3164) | rejected | a Hangul filler alone |
| U+115F U+1160 | rejected | fillers only |
| `⠀⠀` (U+2800 twice) | rejected | braille blanks |
| U+0301 | rejected | a combining mark with no base |
| U+3164, space, U+3164 | rejected | fillers and a space |
| `山田　太郎`, `김민준`, `é`, `😀`, `مريم` | accepted | ordinary names |
| U+1100 U+1161 | accepted | Hangul written with conjoining jamo |
| U+3164 `a` | accepted | one visible character is enough |

The database does not check this rule: PostgreSQL regular expressions have
no Unicode categories, and names stored before the rule must keep working.
Identity is the gate for new names.

**Showing names.** Views show a member through `view.MemberName`, which puts
the display name in its own `<bdi>` so right-to-left text cannot reorder the
text around it. A blank-looking display name — including one stored before
this rule — is converted to an empty name by web when building the channel
page, members panel and message view models, and shown as `@handle` alone.
Channel message authors use the same component for the page and stream.
Member references in `chat_v1` bodies ([decision 39](../decisions/39-message-bodies-use-a-small-frozen-markup.md))
store only the member's UUID and show the current `Display name @handle` through `view.MemberName`,
resolved through org's directory in the URL's organisation; a member who
cannot be resolved shows a localised placeholder, never the UUID or another
organisation's name.
Only resolved references carry `data-member-id` in HTML. The browser matches
it to the viewer's member ID and outlines their own mentions (#1313),
including live posts, replay, older messages and moves; the server render is
shared by all readers.
The channel page resolves member IDs (authors and references) to handles and
account IDs through org, then looks up display names through identity, in
two batches. The members
page takes account IDs and handles from org's paged directory, then resolves
display names through identity in one batch.

## Member search

`org.MemberSearch.Search` takes the resolved URL membership and returns at most
8 candidates (member ID, handle, display name) from that organisation only.
Trim the query, drop one leading `@`, then normalise to NFC; more than 50 code
points returns none. Both query and names use NFKC and Unicode lower case for
substring matching on display name or handle. Thus `加藤`, `智也`, `tomo` and
`ｔｏｍｏ` find `加藤 智也 @tomoya`; reading-based search is not supported.
Blank-looking display names count as empty, including in the returned candidate.
Handle prefixes, display-name prefixes and prefixes of whitespace-separated
display-name parts rank equally first, then other substrings; ties use handle
order. Empty queries (including `@`) return the first 8 by handle.
Reserved words (`all`, `everyone`, `here`, `channel`) are ordinary search text:
only members whose names contain them can match; there are no group candidates.
`GET /organizations/{slug}/mention-candidates?q=` serves the search as a
no-store listbox fragment, behind membership and an independent per-member
[rate limit](../architecture/rate-limits.md#member-admission-for-mention-candidates).
The planned composer picker (#1312) will insert typed `@handle` text.

## Typed mentions

Posting converts `@handle`, ignoring ASCII case, only in the parser's plain
text: at the start of a line or after whitespace or `(`, followed by a valid
handle ending in a letter or digit. Trailing punctuation stays outside.
Code spans, closed fences, escapes, URLs and e-mail addresses stay literal;
an unclosed fence is ordinary text. Only the URL organisation's members
resolve, through org's existing handle-ordered listing, fetched once only
when needed. Unknown, foreign and reserved handles stay literal, with no error.

Resolution happens at posting: if members swap handles between a pick and
send, the mention follows the handle. Storage keeps only the resolved ID;
later renames show the current name. A same-key retry verifies the normalized
typed input's stored byte length and SHA-256 digest, without resolving today's
owner again. Different typed input, including another handle or casing, channel
or quote returns 409. Recovery removes only a prefix verified by that proof;
without it the draft stays ([posting](../architecture/posting.md)).

## Handles

Trimmed; any non-ASCII character is rejected *before* lower-casing (the
Kelvin sign U+212A must not become `k`), then ASCII letters are lower-cased.
2–32 characters, `a-z` first, `a-z0-9` last, `a-z0-9_.-` between; not
`everyone`, `here`, `channel` or `all`. Stored in this form, so `Tomoya` and
`tomoya` collide; the database checks the same format and reserved words.
Members that existed before handles got `member-<n>`, numbered per
organisation in id order.

A handle can change safely, since stored references use IDs: a member changes
only their own (`org.HandleChanger`), and a released handle is free for anyone at
once.
