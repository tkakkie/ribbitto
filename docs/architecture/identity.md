# Sessions and signing in

`internal/identity.Sessions` owns the session lifecycle; `internal/web/middleware`
connects it to HTTP. `internal/identity` also owns `Account` and the email,
password and display-name rules ([validation](../domain/validation.md)).

**Account creation in a caller's transaction.** `org.AccountCreator` and its
`AccountCreatorIn` factory accept validated, normalised values and a password
hash. `identitypg.AccountCreatorIn` binds identity's store to the supplied
`platform.Tx`; it never opens, commits or rolls back. Composition roots adapt
it to org's factory with a named function. The store maps `account_email_key` to
`identity.ErrEmailTaken` and every `account_email_*` CHECK to
`identity.ErrInvalidEmail`; other errors remain wrapped PostgreSQL errors.
These distinct errors preserve sign-up's field mappings and let setup
re-check completion after rollback for either. `org.SignUp` uses the creator
and maps identity's errors to `org.ErrEmailTaken` and an `email` field error;
`org.Setup` uses it too and maps both to an `email` field error.
Their handlers replace sessions as a separate step after account creation.

- **Create** (inside `Replace`, below): 32 random bytes from `crypto/rand` are
  the token, returned once as unpadded base64url for the cookie. Only the
  token's SHA-256 hash is stored, with an absolute expiry 30 days ahead;
  using the session never extends it.
- **Resolve** (every request): the token is decoded strictly, hashed and
  looked up together with its account, only while `expires_at` is after the
  application clock's now. A malformed, unknown, deleted or expired token is
  `identity.ErrNoSession`, meaning signed out; a store error stays an error, so
  a database outage is not mistaken for a sign-out.
- **Delete** (sign-out): the row goes, so the token stops working at once.
- **Open streams** (#207): `Resolve` also returns the session's id and
  expiry, which the session middleware puts on the request. `Delete` and
  `Replace` resolve the incoming token before changing sessions. After success,
  they tell the `SessionCanceller` (the realtime hub, wired in `cmd/ribbitto`)
  the deleted session's id. On a mutation error they cancel the resolved id
  anyway: the deletion may have committed even if the store returned no id.
  This fails closed; after a rollback the streams reconnect and re-check the
  still-valid session. Other sessions stay connected. `identity.ErrNoSession`
  (empty, malformed, unknown, deleted or expired token) preserves idempotent
  sign-out and replacement creation; an operational lookup error aborts
  without changing sessions or cancelling streams. This extra lookup happens
  only when ending or replacing a session, including setup and sign-up,
  whose availability-first routing stays unchanged.
- **Clean-up:** `ribbitto serve` deletes expired rows at start and then
  hourly until shutdown. Expired sessions are already rejected; this only
  keeps the table small.

Session token hashes must never be logged; PostgreSQL errors from session
creation and replacement omit the detail, which could contain the hash.

**Signing in** (`identity.SignIn`). The email is normalised and looked up.
An unknown email still runs one Argon2id verification against a dummy hash
made with the current parameters and gets the same `ErrInvalidCredentials`
as a wrong password, which makes timing-based discovery of accounts much
harder. On success the session is **replaced** (`Sessions.Replace`): one
transaction deletes the session named by the token the browser sent (if
any) and inserts the new one, so a token that existed before sign-in never
becomes signed in (session fixation). Rejected credentials change nothing;
a replacement error ends the previous session's streams as described above.
Signing out deletes the session row.

**One rule for every flow that issues a session:** sign-in, sign-up and
setup all pass the incoming session cookie to `Sessions.Replace`, so the new
session ends the browser's previous one in the same transaction.
`internal/web` sees sessions only through
`SessionReplacer`, which has no plain `Create`, so a later flow (invitation
acceptance, password reset) cannot forget this.

**Cookie.** The token travels in `__Host-session` with `Path=/`, no
`Domain`, `HttpOnly`, `Secure`, `SameSite=Lax` and a `Max-Age` matching the
session's expiry; `SetSessionCookie` and `ClearSessionCookie` are the only
code that writes it. `Lax` keeps the cookie on top-level navigations (a link
from email or chat), and cross-origin POSTs are stopped separately
([`request-flow.md`](request-flow.md#middleware-order)).

**Sign-in and sign-out pages** (`internal/web/signin.go`). `GET /signin`
shows the form (a signed-in request is redirected to `/`). `POST /signin`
calls `identity.SignIn` (above) with the cookie the browser sent, so that
session is the one replaced; on success it sets the new cookie and
redirects to `/` with 303. An unknown email and a wrong password get the
same page, status (422) and message; the typed email is kept and the
password never echoed. An email rejected by `identity.ValidateEmail` or an
empty password gets 422 with its own message, without revealing whether an
account exists. A busy hasher answers 503. `POST /signout` deletes
the session row, clears the cookie and redirects to `/signin`; there is no
GET sign-out. `cmd/ribbitto` creates the process's one `identity.Hasher` here
and shares it with every authentication use case.
