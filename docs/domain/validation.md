# Validation

`internal/identity` owns the email, password and display-name rules
(`ValidateEmail`, `ValidatePassword`, `ValidateDisplayName`,
`IsBlankLookingName`); `internal/org` owns `ValidateOrganizationName`,
`ValidateSlug` and `ValidateHandle`; `internal/conversation` owns
`ValidateChannelName`. `internal/domain` validates the remaining
fields until their modules take them. These rules validate UTF-8 text.
Email, display, organisation and channel names are trimmed and normalised to
NFC; email is lower-cased first. Lengths count code points after
normalisation, except email (bytes) and slug (ASCII). Controls including NUL fail before trimming. These fields then
require `unicode.IsPrint`, rejecting format characters (zero-width spaces,
RTL overrides, BOMs, soft hyphens) and line/paragraph separators. Names allow
only ASCII and ideographic (U+3000) spaces; email allows none. The database
also requires account email and display name to be NFC.

- **Email:** exactly one `@`, nonempty parts on both sides, at most 254 bytes
  after normalisation.
- **Display name:** 1–50 characters after normalisation, not blank-looking
  ([`names.md`](names.md#display-names)).
- **Password:** 15–128 characters, preserved as entered, no composition rules.
- **Organisation name:** 1–100 characters after normalisation.
- **Channel name:** as organisation names, but 1–80 characters.
- **Message body:** plain text, 1–4000 code points ([`messages.md`](messages.md)).
- **Slug:** 1–63 characters from `a-z0-9-`, no leading or trailing `-`.
- **Handle:** 2–32 ASCII characters, unique per organisation ignoring case
  ([`names.md`](names.md#handles)).

Sessions store a unique 32-byte token hash and expire after their creation.
Deleting an account cascades to sessions. Membership references restrict
account and organisation deletion; membership is unique per organisation and
account, and `joined_event_seq` starts at 1.

**Names and identity.** Display names and handles are for people only;
**`member_id`** is the only identifier the system trusts, for permissions,
mentions and stored references. See [`names.md`](names.md). Message history resolves current author names
in batches without storing names or HTML on messages.
