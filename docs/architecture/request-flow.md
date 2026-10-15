# Request flow

```mermaid
sequenceDiagram
  participant B as Browser
  participant W as web (handler)
  participant A as use case (module root)
  participant P as PostgreSQL store (module)
  B->>W: HTTP request (/organizations/{slug}/…)
  W->>W: parse and validate input, resolve session → account
  W->>A: call use case with plain arguments
  A->>A: resolve member of {slug}, authorize
  A->>P: repository calls inside one transaction
  P-->>A: domain values
  A-->>W: plain result struct
  W-->>B: HTML (full page or htmx fragment)
```

The organisation comes from the URL, never from a request body. Setup
creates it; installation-wide sign-up and `/` resolve it from the setup row,
never from the request. An
account that is not a member of that organisation gets 404, not 403, so the
existence of an organisation or channel is not revealed.

**Organisation routes.** Every page under `/organizations/{slug}/` is listed in
`orgRoutes` (`internal/web/org.go`) and registered by `registerOrgRoutes`,
which calls `org.Authorizer.Member` with the signed-in account and the
slug before the page's handler runs and passes it the resolved membership;
handlers never read `{slug}` themselves. A signed-out request, an unknown
slug, a non-member and a member of another organisation all get the same
404 — a signed-out request is not redirected to sign-in, because a redirect
would reveal which slugs exist. The same list drives
`TestOrgRoutesAgainstPostgreSQL`, so a new route is covered by the 404 tests
as soon as it is added. `GET /` redirects a member of the setup
organisation to its page (`org.Authorizer.HomeSlug`); everyone else sees
the home page.

The channel handlers in `internal/web/channel.go` call `conversation.Channels`
with that membership; they never query the database directly:

| Route | Result |
|---|---|
| `GET /organizations/{slug}/` | 303 to `conversation.Channels.Default`, found by `is_default`; a missing default is logged and answers 500 |
| `GET /organizations/{slug}/channels` | The channel list page (#969): the sidebar as the page, nesting the topics of `?channel=<channel-id>` or else the default channel, with a sidebar-only stream; a malformed query or a malformed, unknown or other organisation's channel answers 404. The list is the page's `<main>`; below the `desk` breakpoint it fills the screen above the bottom navigation, and at desktop width a block beside it asks the member to open a channel |
| `GET /organizations/{slug}/channels/{channel-id}` | Channel page and sidebar; malformed, unknown and other organisations' UUIDs answer 404. `?before=<event_seq>` shows the older page and `?around=<event_seq>` the older page holding that position; a bound that is not one positive integer, both bounds, or a malformed query, answers 400 |
| `GET …/channels/{channel-id}/topics/{topic-id}` | Topic view and sidebar for that channel's topic; malformed, unknown, other-organisation or other-channel UUIDs answer 404, with the same `?before=` and `?around=` paging and `unread` divider query |
| `GET /organizations/{slug}/messages/{message-id}` | One message ([decision 36](../decisions/36-one-message-has-a-stable-url.md)): 303 to its current topic view with `#message-<hex>`, as the latest page when the message is on it, otherwise with `?around=<event_seq>` (an older page); a malformed or unknown UUID, another organisation's message or one the member may not read answers 404 |
| `GET …/channels/{channel-id}[/topics/{topic-id}]/members` | The feed or topic page with the members panel: display names and handles, 100 per page in ID order, `?after=<member-id>` for the next page beside the conversation's own `before` and `unread`. Panel and close links carry the position the page renders, error renders included: an older page's `before` or `around` and the divider it shows as `unread`, never the request's other keys. Invalid cursors answer 400; invisible channels or topics and non-members get 404. Its stream adds `presence` |
| `GET …/channels/{channel-id}[/topics/{topic-id}]/overview` | The same page with the overview panel: the channel's name, creation date and topics; the same 404s |
| `POST …/channels/{channel-id}[/topics/{topic-id}]/overview` | Changes the image through `conversation.Channels.SetImage`; unknown keys render an open picker with a visible error (422). Empty stores NULL. A malformed `before` or `unread` answers 400 before anything is stored. Success returns 303 to the same overview with the page's own `before` and `unread` (other keys dropped), with or without JavaScript; no event |
| `POST /organizations/{slug}/channels/{channel-id}` | Calls `conversation.Posting.PostFromPage` (with `cursor`; malformed `cursor` answers 400) or `PostToTopic`; htmx receives only the reset composer (the message itself arrives over the event stream), otherwise 303 back to the channel; invalid bodies render a field error (422) |
| `POST …/channels/{channel-id}/topics/{topic-id}` | Posts to that topic through `PostFromPage` (with `cursor`; malformed `cursor` answers 400) or `PostToTopic`; unknown or other-channel topics answer 404, invalid bodies render the topic page with a field error (422), and success returns 303 back to the topic view |
| `POST …/channels/{channel-id}/branch` | Moves selected messages through `conversation.Brancher.Branch` after the member's branch limiter (429 with `Retry-After: 6`); unknown channel or topic answers 404, conflicts 409 and invalid input 422 (`BranchFeedback` for htmx, full page otherwise); success redirects to the destination topic view (`HX-Redirect` or 303) |
| `POST …/channels/{channel-id}[/topics/{topic-id}]/read` | Advances feed or topic read state through `unread.Reading` up to `cursor` after the read limiter (429 with `Retry-After: 1`) and scope lookup (404); missing or invalid `cursor`, a malformed query, or any `before` query or form parameter answers 400; 204 for htmx or 303 to the conversation |
| `POST /organizations/{slug}/channels/{channel-id}/topics/{topic-id}/typing` | A typing signal with one opaque `composer` generation ([typing](typing.md#http-ingress-and-posting)): after membership, the member's shared limiter (429), then the membership-scoped channel and topic lookups (404); an empty 204 whether or not the signal changed state. Successful posts stop the poster's typing in the returned message's scope |
| `POST /organizations/{slug}/sidebar/resync` | After the member's resync limiter (429 with `Retry-After: 1`), publishes a member read-change wake (`unread.Changes`) so the member's streams recount the sidebar, and answers 204 |
| `GET /organizations/{slug}/events` | Organisation stream, filtered by [interests](stream-scope.md#stream-scope); malformed queries answer 400, an invisible channel or topic 404 before registration. A present `Sec-Fetch-Site` other than a single `same-origin` value answers 403 (absent is allowed), an ended session 404, either stream cap 429, shutdown 503. Cancellation before the first write answers 404 (session ended) or 503 (shutdown); see [streaming](streaming.md) and [stream limits](stream-limits.md) |
| `POST /organizations/{slug}/channels` | Creates a channel with an optional bundled image and answers 303 to its UUID URL; invalid images, invalid or duplicate names, or the organisation cap render the list page with a field error (422), preserving the name and image, with the default channel expanded; admission after membership and before parsing uses the independent creation budget (429 with `Retry-After: 60`) |

The creation form inherits the shared CSRF protection and body limit.
Its organisation comes only from the resolved membership. Names are escaped
by templ and never become URL identifiers.

## Server timeouts

In `newServer` (`cmd/ribbitto`):
- 10 s to read the request line and headers.
- 30 s to read a whole request, headers and body. Bodies are capped at 64 KiB.
- 60 s for a keep-alive connection to wait for its next request.
- 60 s to write a response (`WriteTimeout`). `net/http` starts it when the request headers have been read, so it also covers reading the body and running the handler; it is longer than the read limit so a slow but legitimate request still gets its answer.

Without these limits, a client could hold a connection for as long as it liked: idle between requests, with a body started and never finished, or by never reading the response. That includes an unread body that `net/http` discards after a 404 or 429.

**The rule:** ordinary HTTP responses have a bounded write. A streaming endpoint has its own, explicit bounded-write policy instead of the server's: M3's SSE sets a finite deadline before each write (see [`stream-limits.md`](stream-limits.md)). A future route that must read a long request body sets its own read deadline through `http.ResponseController`.

ribbitto keeps these limits itself and does not rely on a reverse proxy for them ([decision 15](../decisions/15-every-ordinary-response-has-a-bounded-write-ribbitto-does-not-rely-on-a-proxy-for-it.md)). A proxy in front (Caddy by default, or nginx, Traefik and others) adds defense in depth: TLS, keeping the backend unreachable from outside, and connection and slow-client limits.

## Middleware order

Outermost first:

1. `http.CrossOriginProtection` on every route. It rejects cross-origin
   requests using `Sec-Fetch-Site`, or `Origin` against `Host`, but lets GET,
   HEAD and OPTIONS through — so **every route that changes state is a
   POST**, and GET handlers never change state.
2. `middleware.LimitBody`: every request body is capped at 64 KiB. Reading
   past it fails with `*http.MaxBytesError`, which handlers answer with 413;
   a route that answers without reading the body (a closed route's 404) is
   unaffected.
3. On everything except `/static/` and `/healthz`: security headers
   ([`rendering.md`](rendering.md#content-security-policy)), then i18n.
   `/static/` sets its own `nosniff` and restrictive CSP.
4. On each **registered** HTML route (the `sessionMux` in `NewHandler`
   wraps routes one by one; `/setup` and `/signup` are registered without
   it, so their availability is decided first): `middleware.Session`. An unknown path or
   method gets its plain 404 or 405 without a session lookup, so it costs
   no query and stays 404 while the database is down. The middleware
   resolves the cookie and puts the account and the session in the context
   (`middleware.Account`, `middleware.CurrentSession`; streams register
   under the session). A token that signs nobody in means signed out
   and the cookie is cleared; a store error answers 500 and keeps the
   cookie, so an outage does not sign everyone out. Responses to signed-in
   requests carry `Cache-Control: no-store`.
