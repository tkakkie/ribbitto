# 17. Full English words and stable identifiers in page URLs

**Decided:** page URL segments use full English words; collections are
plural (`organizations`, `channels`, `members`, `messages`). A resource
inside a collection uses its stable identifier, never its display name:
the organisation's `slug`, a channel's UUID. This rule also applies to
later member, message and settings routes. Organisation pages start at
`/organizations/{slug}/`; channel pages will use
`/organizations/{slug}/channels/{channel-id}` (#76). The existing
`/signin`, `/signup` and `/setup` stay as they are; a JSON API's URL scheme
is outside this decision.
**Why:** full words make URLs easier to read and guess; stable identifiers
keep links valid when display names change. ribbitto is unreleased, so the
old `/o/` prefix is removed without redirects (#132).
**Considered:** abbreviated `/o/` and `/c/` segments (less readable);
display names or readable channel slugs (unnecessary naming and rename
rules); redirects from the old prefix (nothing deployed needs them).
