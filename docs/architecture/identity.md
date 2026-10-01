# Sessions and signing in

`internal/app/auth.Sessions` owns the session lifecycle; `internal/web/middleware`
connects it to HTTP.

- **Create** (inside `Replace`, below): 32 random bytes from `crypto/rand` are
  the token, returned once as unpadded base64url for the cookie. Only the
  token's SHA-256 hash is stored, with an absolute expiry 30 days ahead;
  using the session never extends it.
- **Resolve** (every request): the token is decoded strictly, hashed and
  looked up together with its account, only while `expires_at` is after the
  application clock's now. A malformed, unknown, deleted or expired token is
  `auth.ErrNoSession`, meaning signed out; a store error stays an error, so
  a database outage is not mistaken for a sign-out.
- **Delete** (sign-out): the row goes, so the token stops working at once.
- **Open streams** (#207): `Resolve` also returns the session's id and
  expiry, which the session middleware puts on the request. After a delete
  or a successful `Replace`, `auth.Sessions` tells its `SessionCanceller`
  (the realtime hub, wired in `cmd/ribbitto`) the ended session's id, and
  that session's event streams end; a failed `Replace` ends nothing.
- **Clean-up:** `ribbitto serve` deletes expired rows at start and then
  hourly until shutdown. Expired sessions are already rejected; this only
  keeps the table small.

Session token hashes must never be logged; PostgreSQL errors from session
creation and replacement omit the detail, which could contain the hash.

**Signing in** (`auth.SignIn`). The email is normalised and looked up.
An unknown email still runs one Argon2id verification against a dummy hash
made with the current parameters and gets the same `ErrInvalidCredentials`
as a wrong password, which makes timing-based discovery of accounts much
harder. On success the session is **replaced** (`Sessions.Replace`): one
transaction deletes the session named by the token the browser sent (if
any) and inserts the new one, so a token that existed before sign-in never
becomes signed in (session fixation), and a failed sign-in changes nothing
— the browser keeps the session it had. Signing out deletes the session
row.

**One rule for every flow that issues a session:** sign-in, sign-up and
setup all pass the incoming session cookie to `Sessions.Replace`, so the new
session ends the browser's previous one in the same transaction, and a
failure leaves it untouched. `internal/web` sees sessions only through
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
calls `auth.SignIn` (above) with the cookie the browser sent, so that
session is the one replaced; on success it sets the new cookie and
redirects to `/` with 303. An unknown email and a wrong password get the
same page, status (422) and message; the typed email is kept and the
password never echoed. An email rejected by `domain.ValidateEmail` or an
empty password gets 422 with its own message, without revealing whether an
account exists. A busy hasher answers 503. `POST /signout` deletes
the session row, clears the cookie and redirects to `/signin`; there is no
GET sign-out. `cmd/ribbitto` creates the process's one `auth.Hasher` here
and shares it with every authentication use case.
