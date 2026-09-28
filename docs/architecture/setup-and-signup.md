# First-run setup and sign-up

`internal/app/setup` checks the configured token and validates all fields
before using the shared `auth.Hasher`. Its store interface requires atomic
creation; `postgres.SetupStore` implements it with one transaction for the
organisation, first sequence, owner account, membership (with the owner's
handle) and setup marker.
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
`auth.Sessions.Replace` ([`identity.md`](identity.md)), sets the shared session cookie and redirects
to `/` with 303. Setup and sign-in share the process's single password hasher.

`internal/app/signup.Open` gates registration and the sign-in link on
`RIBBITTO_SIGNUP=on` and completed setup. Its separate handler and form share
the process hasher; one transaction reads the setup organisation, takes
its next sequence first, then inserts the account and member, with the
handle the form asked for. Duplicate email or a handle already used in the
organisation rolls back everything and takes no sequence; the unique
constraint decides concurrent claims, and a taken handle is a field error
(422). Success signs the new account in through
`Sessions.Replace` and redirects to `/` with 303; invalid input returns 422 and a busy hasher returns 503.
