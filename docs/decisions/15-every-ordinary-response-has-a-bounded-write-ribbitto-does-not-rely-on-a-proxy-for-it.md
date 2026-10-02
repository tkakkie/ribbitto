# 15. Every ordinary response has a bounded write; ribbitto does not rely on a proxy for it

**Decided:** the HTTP server sets a finite `WriteTimeout` (60 s), so a
client that stops reading releases its connection and handler. A streaming
endpoint does not inherit it: it has its own explicit bounded-write policy.
M3's SSE sets a finite deadline before each write with
`http.ResponseController.SetWriteDeadline`. SSE is not a reason to leave
ordinary responses unbounded. ribbitto's own HTTP timeouts and write
deadlines keep it safe without any particular reverse proxy. A proxy (Caddy
in the recommended setup, or nginx, Traefik and others) adds defense in
depth: TLS, keeping the backend unreachable from outside, and connection
and slow-client limits. Static assets serve a request for several ranges in
full, as a small extra limit. This supersedes the "no global
`WriteTimeout`" choice made in #103.
**Why:** without a write deadline, a client that sent a request and never
read the response held a connection, a goroutine and its handler for as
long as the TCP connection lived. Many ranges in one `Range` header made
each such response larger (#125, found by a security scan). ribbitto is
open source and may run behind any proxy, or none.
**Considered:** no `WriteTimeout` until M3, relying on the proxy (the
earlier choice; unsafe without one); limiting `Range` alone (it shrinks
responses but leaves them unbounded in time).
