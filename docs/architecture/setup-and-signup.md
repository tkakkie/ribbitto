# First-run setup and sign-up

## First-run setup

`setup` is installation-wide state, read without an organisation filter.
Its `organization_id` only names the organisation setup created; it does
not make the row organisation-owned. Its primary key is a boolean fixed to
`true`, not a UUIDv7. Completion stays recorded even if other organisations
are created later.

A nonempty configured setup token authorizes setup: SHA-256 hashes of the
configured and submitted tokens are compared in constant time. All fields
are validated before password hashing. One transaction inserts the
organisation, takes its next `event_seq` (1), creates the account
(Argon2id hash), its owner membership at that sequence, the default
channel and the setup row. Concurrent losers hit the singleton key and roll back;
repeating setup is 404.

`org.Setup` checks the configured token, then whether setup is open, and
validates all fields before using the shared `identity.Hasher`. Its
`org.SetupStore` interface requires atomic creation; `postgres.SetupStore`
implements it (until step 3.12) with one transaction for the
organisation, first sequence, owner account, membership (with the owner's
handle), default channel ([`channels.md`](../domain/channels.md)) and setup marker.
`cmd/ribbitto` validates `RIBBITTO_SETUP_TOKEN` before opening the database:
empty disables setup, and a non-empty value needs at least 32 characters.
When disabled, `/setup` is not registered at all, so GET and POST are the
router's plain 404, without calling setup or looking up a session. When
enabled, `/setup` is registered outside the session middleware (setup needs
no signed-in account), so whether setup is open is decided first, whatever
the session cookie or the state of the session store.
Otherwise the handler checks `Open` before rendering or accepting a form.
POST calls `Complete` (#31); closed setup (including a concurrent completion)
returns 404, invalid fields or token return 422 without echoing secrets, and
a busy hasher returns 503. Success signs the owner in through
`identity.Sessions.Replace` ([`identity.md`](identity.md)), sets the shared session cookie and redirects
to `/` with 303. Setup and sign-in share the process's single password hasher.

## Sign-up

`org.SignUp` (`NewSignUp`, built by `orgpg.NewSignUp`) shares
`ValidationErrors` and one private account-field validator with `org.Setup`:
display name, handle, email and password keep their rules and keys; setup also
validates organisation name and slug. Each flow checks its gate, then every
field, then hashes. `SignUp.Open` gates registration and the sign-in link on
`RIBBITTO_SIGNUP=on` and completed setup. Its separate handler and form share
the process hasher. Through the injected `TxRunner`, the root owns one
transaction: read the setup organisation, take its sequence first, create
identity's account through `AccountCreatorIn`, insert org's member, then
append `member.joined` through `EventAppenderIn`. Org declares both factories;
wiring adapts the providers with closures, all bound to the same transaction.
The member gets the validated handle and sequence. Database email and handle
rejections become typed conflicts or field errors without inspecting pgconn.
Duplicate email or a handle already used in the organisation rolls back
everything and takes no sequence; the unique constraint decides concurrent
claims, and a taken handle is a field error (422), using `org.ErrEmailTaken`
and the shared `org.ErrHandleTaken`. `org.ErrSignUpClosed` means the operator
disabled sign-up or setup is incomplete. Success signs the new account in
through `Sessions.Replace` and redirects to `/` with 303; invalid input
returns 422 and a busy hasher returns 503.
