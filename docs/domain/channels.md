# Channels

What a channel is, how it is identified and the rules about the default
channel. Read this before changing channel creation, setup, or anything that
finds, renames, archives or deletes channels.

**Keep it current:** update this file in the same pull request whenever
these rules change. Validation of the name is in
[`validation.md`](validation.md#validation).

## Identity and names

A channel is identified by its `id` (UUIDv7) in routes, references and
code; the name is only a display name and may contain any printable text,
Japanese included. Names are unique per organisation
(`UNIQUE (organization_id, name)`) only because channels sit flat under the
organisation today, so two channels with one name would confuse people. If
namespaces arrive (categories, projects), uniqueness can narrow to them, for
example `(organization_id, parent_id, name)`; nothing may rely on a name
being unique across the organisation. A duplicate name is a user-facing
error. Every member may create channels: in the MVP all channels are public.

## The default channel

**Every organisation has exactly one default channel.** It is found by
`is_default`, never by its name; `general` is only the name it starts
with. The database can only guarantee *at most one*, so three things keep
it at exactly one:

- **at most one:** the partial unique index
  `UNIQUE (organization_id) WHERE is_default`;
- **at least one:** first-run setup creates the organisation and its
  default channel in one transaction (a failure rolls both back), and
  migration `00006` gave every organisation that existed before it a
  default — turning an existing non-default `general` into the default
  rather than inserting a second one;
- **nothing removes it:** no operation deletes a channel or clears
  `is_default`, and a channel a member creates is never the default.

A missing default is a broken invariant: the channel use case reports it as
an error, not as "not found", so it is not hidden behind a 404.

Future deletion, archiving or changing the default must leave exactly one
default at commit: in one transaction, lock the organisation's row (as
taking `event_seq` does) so concurrent changes are serialised, clear the old
default, then set the new one. Setting the new one first would violate the
unique index at once.

## Topics

Every channel will have topics, including exactly one default topic, which
is unrelated to the default channel above: see [`topics.md`](topics.md)
*(planned)*.
