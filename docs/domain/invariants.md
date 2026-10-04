# Invariants

Rules that must always hold, for all code and migrations. Terms are in the
[glossary](README.md#glossary); tables are in [`entities.md`](entities.md).

**Keep it current:** update this file in the same pull request whenever an
invariant changes. Keep the numbers stable: other documents cite them.

1. **Everything an organisation owns carries `organization_id`**, and every
   query on it filters by `organization_id` — including simple examples in
   documentation.
2. **References inside an organisation are composite foreign keys that
   include `organization_id`**, so a row can never point at another
   organisation's data even if an id is guessed.
3. **Organisation-owned data refers to `member`, never to `account`.**
4. **The organisation comes from the URL** (`/organizations/{slug}/…`), never
   from a request body. Setup creates the organisation; installation-wide sign-up
   and `/` resolve it from the setup row, never from the request. An account
   that is not a member gets **404**. Enforced by `registerOrgRoutes` in
   `internal/web/org.go`.
5. **Authorization is decided only in `internal/org`.** Handlers and the
   real-time loop (`realtime.Stream.Run`) call it; they never
   re-implement it. The entry point is
   `internal/org`: `Authorizer.Member` turns the signed-in account and
   the slug from the URL into a membership, with one not-found error for an
   unknown slug, a non-member and a signed-out caller. `orgpg.NewAuthorizer`
   builds it with org's store, which reads memberships and resolves the home
   organisation through the setup row.
6. **`organization.event_seq` only increases, without gaps.** It is taken
   first in the writing transaction, and the same value is stored in both
   `event_log.seq` and the entity's own `event_seq` (for messages) or
   `joined_event_seq` (for members). Log rows are gap-free above
   `event_log_boundary_seq`, the highest sequence no longer in the log.
   Existing organisations start logging after their migration-time `event_seq`;
   new ones start at boundary 0. Retention raises it, only past a contiguous
   prefix it deleted: never past a sequence still in the log. Valid cursors range from
   this boundary through the committed `event_seq`, inclusive. A cursor outside
   these bounds requires `reset`, including one above `event_seq` after a database
   restore made with ribbitto stopped; a cursor equal to `event_seq` waits for new events.
7. **Session tokens are never stored.** Only their SHA-256 hash is; passwords
   are stored only as Argon2id hashes.
8. **An email address proves nothing about who owns an account.** M1 does
   not verify email addresses, so anyone can create an account with someone
   else's address. Therefore:
   - an unverified address is never evidence of identity, neither for
     linking an external identity (OIDC, SAML, SCIM) to an account nor for
     granting a membership (in M1, memberships come only from setup and
     sign-up);
   - an external identity is never linked to an existing account because
     the email addresses match.

   Sign-up's duplicate-email response is an accepted trade-off
   ([decision 13](../decisions/13-sign-up-may-reveal-that-an-email-address-is-registered.md)).
9. **Every channel message is in exactly one topic of its own
   channel, and every channel has exactly one default topic.** Composite
   foreign keys keep both in the same organisation and channel; branching
   moves messages, never copies them, and keeps their `id` and `event_seq`
   ([`topics.md`](topics.md)).
10. *(planned)* **Replies reference existing messages in the same
    organisation and channel with lower `event_seq`.** A composite foreign
    key enforces scope; immutable references prevent cycles. Chain reads
    require channel authorization and leave read positions unchanged
    ([`replies.md`](replies.md)).

These are **requirements for all code and migrations**, not a description
of what is implemented today: `organization`, `account`, `session`,
`member`, `setup`, `channel`, `message`, `event_log` and `topic` have tables; setup authorization is implemented, and
organisation routes go through `internal/org`. Every migration that adds an
organisation-owned table must include `organization_id` and
composite foreign keys (1–3), and every use case must be covered by tests
for 4–5 as it is written. Row-level security in PostgreSQL is planned as a
second line of defence after the MVP.
