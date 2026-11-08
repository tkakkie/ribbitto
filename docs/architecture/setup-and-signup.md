# First-run setup and sign-up

## First-run setup

`setup` is installation-wide state, read without an organisation filter.
Its `organization_id` only names the organisation setup created; it does
not make the row organisation-owned. Its primary key is a boolean fixed to
`true`, not a UUIDv7. Completion stays recorded even if other organisations
are created later.

A nonempty configured setup token authorizes setup: SHA-256 hashes of the
configured and submitted tokens are compared in constant time. `org.Setup`
checks the token, then whether setup is open, then validates all fields before
hashing the NFC password. The root owns one transaction that inserts the
organisation, takes its next `event_seq` (1), creates the account
(Argon2id hash), its owner membership at that sequence, `member.joined`, the
default channel ([`channels.md`](../domain/channels.md)) and the setup row.
Concurrent losers hit the singleton key and roll back;
repeating setup is 404.

After a slug, email, handle or setup-row conflict, setup re-checks completion
after rollback, since a concurrent winner may have collided first: closed means
`ErrSetupCompleted`. While setup is open, a taken or invalid email or slug
and an invalid handle are field errors, a setup-row conflict is
`ErrSetupCompleted`, and a duplicate handle (`ErrHandleTaken`) stays a
wrapped error. A failed re-check uses the same open-setup mapping.
The root never inspects pgconn.

`cmd/ribbitto` validates `RIBBITTO_SETUP_TOKEN` before opening the database:
empty disables setup, and a non-empty value needs at least 32 characters.
When disabled, `/setup` is not registered at all, so GET and POST are the
router's plain 404, without calling setup or looking up a session. When
enabled, `/setup` is registered outside the session middleware (setup needs
no signed-in account), so whether setup is open is decided first, whatever
the session cookie or the state of the session store.
Otherwise the handler checks `Open` before rendering or accepting a form.
Closed setup (including a concurrent completion)
returns 404, invalid fields or token return 422 without echoing secrets, and
a busy hasher returns 503. Success signs the owner in through
`identity.Sessions.Replace` ([`identity.md`](identity.md)), sets the shared session cookie and redirects
to `/` with 303. Setup, sign-up and sign-in share the process's single password hasher.

## Sign-up

Both flows preserve the display-name, handle, email and password rules and
field keys; setup also validates organisation name and slug. Sign-up checks
its gate, then every field, then hashes the NFC password from identity validation.
`SignUp.Open` gates registration and the sign-in link on `RIBBITTO_SIGNUP=on`
and completed setup. The root owns one transaction: read the setup organisation,
take its sequence first, create identity's account, insert org's member, then
append `member.joined`.
The member gets the validated handle and sequence. Database email and handle
rejections become typed conflicts or field errors without inspecting pgconn.
Duplicate email or a handle already used in the organisation rolls back
everything and takes no sequence; the unique constraint decides concurrent
claims. Duplicate email (`org.ErrEmailTaken`) and a taken handle
(`org.ErrHandleTaken`) are errors on their respective fields (422), as are
invalid database email and handle values. `org.ErrSignUpClosed` means the operator
disabled sign-up or setup is incomplete, including a missing setup row in the
transaction.
Success signs the new account in through `Sessions.Replace` and redirects to
`/` with 303; invalid input returns 422 and a busy hasher returns 503.
