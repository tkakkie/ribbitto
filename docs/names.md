# Names

How ribbitto names members: display names, handles and member ids. Read
this before showing a member's name, adding mentions, or changing the rules
for either name.

**Keep it current:** update this file in the same pull request whenever the
display-name or handle rules, or how names are shown, change. The general
validation rules (UTF-8, controls, NFC, printable characters) are in
[`domain.md`](domain.md#validation).

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

1–50 characters after the rules in [`domain.md`](domain.md#validation), and
**not blank-looking**. A name is blank-looking when every character is one
of:

- the ASCII space U+0020 or the ideographic space U+3000;
- the Hangul fillers U+3164, U+FFA0, U+115F and U+1160;
- the braille blank U+2800;
- a combining mark (Unicode category M), which has no base character to
  attach to when nothing else is there.

The rule (`domain.IsBlankLookingName`) is this fixed list of code points and
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
The domain is the gate for new names.

**Showing names.** Views show a member through `view.MemberName`, which puts
the display name in its own `<bdi>` so right-to-left text cannot reorder the
text around it. A blank-looking display name — including one stored before
this rule — is shown as `@handle` alone.

## Handles

Trimmed; any non-ASCII character is rejected *before* lower-casing (the
Kelvin sign U+212A must not become `k`), then ASCII letters are lower-cased.
2–32 characters, `a-z` first, `a-z0-9` last, `a-z0-9_.-` between; not
`everyone`, `here`, `channel` or `all`. Stored in this form, so `Tomoya` and
`tomoya` collide; the database checks the same format and reserved words.
Members that existed before handles got `member-<n>`, numbered per
organisation in id order.

A handle can change safely, since nothing refers to it: a member changes
only their own (`app/member`), and a released handle is free for anyone at
once.
