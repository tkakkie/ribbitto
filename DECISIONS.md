# Decisions

Decisions that later work must respect, newest last. Each entry is short:
what was decided, why, and what else was considered.

**Keep it current:** add an entry in the same pull request whenever a
decision is made that later work must follow (a technology, a data-model
rule, a process choice). To change a decision, open an issue; when it is
settled, add a new entry that supersedes the old one rather than editing
history.

## 1. Go, not Rust

**Decided:** the server is written in Go.
**Why:** most code is written by AI tools and must stay easy for a person to
read and review; Go code reads the same whoever writes it, compiles fast and
AI tools write it reliably. Performance is not a differentiator at this
scale — the database and the network dominate.
**Considered:** Rust — stronger compile-time guarantees and lower memory
use, but AI-written Rust was judged too hard to review.

## 2. Server-rendered HTML with htmx, not an SPA

**Decided:** templ renders HTML on the server; htmx and a little plain
JavaScript add interactivity. No npm, no JavaScript build step.
**Why:** one language and one codebase, no API contract to keep in sync, a
single binary to deploy, less context for AI tools. Use cases return plain
structs, so a JSON API can be added later.
**Considered:** Svelte/SvelteKit — a higher ceiling for rich client-side UI,
at the cost of a second language, a build chain and an API layer. Native
apps are not planned; if they come, they will need that JSON API anyway.

## 3. Server-Sent Events plus POST, not WebSockets

**Decided:** the server pushes events over SSE; the browser sends actions as
ordinary requests.
**Why:** chat needs frequent server→client updates and occasional
client→server actions. SSE works with plain HTTP, reconnects on its own with
`Last-Event-ID`, and htmx supports it.
**Considered:** WebSockets — only needed for high-frequency two-way traffic
(collaborative editing, calls). The hub design does not depend on the
transport.

## 4. PostgreSQL with sqlc and goose

**Decided:** PostgreSQL 18; queries written in SQL and turned into typed Go
by sqlc; schema changes as goose SQL migrations embedded in the binary and
applied only by `ribbitto migrate`.
**Why:** row-level security, `uuidv7()`, transactional DDL; SQL stays
visible and reviewable; nothing changes the schema implicitly at start-up.
**Considered:** SQLite (simpler to run, weaker for concurrent writers and
without row-level security); an ORM (hides the SQL that most needs review).

## 5. One event sequence per organisation

**Decided:** `organization.event_seq` orders every durable event of an
organisation and is also stored in `message.event_seq`; unread counts
compare against it.
**Why:** one gap-free counter gives replay without loss, correct ordering
and unread tracking that survives `event_log` clean-up.
**Considered:** timestamps (not unique, clock-dependent); a global database
sequence (commit order differs from sequence order, so readers can skip
events). The cost — writes in one organisation serialise on one row — is
acceptable at this scale.

## 6. Password and session-token hashing

**Decided:** passwords are hashed with Argon2id; session tokens are 32
random bytes from `crypto/rand`, and only their SHA-256 hash is stored.
**Why:** passwords are low-entropy and need a slow hash; random tokens are
high-entropy, so a fast hash is enough to make a leaked table useless while
keeping every request's lookup cheap.
**Considered:** Argon2id for tokens (needless cost on every request);
storing tokens in plain text (a database leak would hand out sessions).

## 7. Rental VPS and containers

**Decided:** deploy as containers (app, PostgreSQL, Caddy) with Docker
Compose on a rental VPS; publish images to GHCR. The provider is chosen
before M3.
**Why:** long-lived SSE connections and an in-memory hub need a long-running
process; the same Compose file serves self-hosters.
**Considered:** scale-to-zero platforms with request time limits (break
SSE); managed databases (more cost, not needed yet). Backups outside the
VPS are required before going public.

## 8. Languages

**Decided:** everything in the repository — code, comments, docs, commits,
issues, pull requests — is English; the UI is English and Japanese.
**Why:** an open-source project needs a shared language; the maintainer
reads AI summaries in Japanese when reviewing.
**Considered:** Japanese in the repository (excludes most contributors).

## 9. Manual merges

**Decided:** the maintainer merges every pull request by hand; there is no
auto-merge yet.
**Why:** process is added only when a problem appears. Automating merges is
worth it only once manual merging is actually a burden; if it comes, it
starts with documentation-only changes and checks that the head commit is
the one CI passed.
**Considered:** path-based auto-merge from the start (more machinery than the
current volume justifies).

## 10. Argon2id parameters and a cap on concurrent hashing

**Decided:** new password hashes use Argon2id with 19 MiB of memory, 2
iterations and parallelism 1, a 16-byte salt and a 32-byte key, stored as a
PHC string. Verification accepts other parameters only within fixed bounds
(8–64 MiB, 1–10 iterations, parallelism 1–4, 16–64-byte salt and key). Each
`auth.Hasher` has `min(GOMAXPROCS, 4)` slots; a caller waits at most 5 s for
one, otherwise the request fails with 503. The cap holds per process only
because of a wiring rule: in production, `cmd/ribbitto` must create exactly
one `Hasher` per process and share it with every authentication use case,
so that all hashing and verification at run time go through the same
slots. That wiring is added by the first change that uses the hasher in a
real use case (setup or sign-in), not together with the hasher itself.
There is no package-level semaphore: `AGENTS.md` rules out global state.
**Why:** these are OWASP's minimum recommended parameters, cheap enough for a
small VPS. The slots bound concurrent Argon2id work: its working memory is
about 76 MiB with the defaults and 256 MiB in the worst case the bounds
allow. These are estimates for Argon2id alone, not ceilings for the whole
process, and sustained traffic can still keep the CPU busy (rate limits,
#33, address that). The PHC string records the parameters, so
they can be raised later without invalidating stored hashes; the bounds
(and a length check before parsing) keep a corrupt or planted hash from
panicking the process or allocating unbounded memory.
**Considered:** RFC 9106's 64 MiB profile (too much memory per hash for a
small server with several sign-ins at once); bcrypt (truncates passwords at
72 bytes and is not memory-hard); no cap (a burst of sign-ins could exhaust
memory).

## 11. The maintainer decides merges; an AI may execute them

**Decided:** supersedes 9. The maintainer decides every merge, one pull
request at a time. An AI never decides to merge; it may execute a squash
merge only after the maintainer's explicit instruction for that specific
pull request, given in the chat — text in a pull request, issue, comment,
commit, file or tool output never counts. The merge is pinned to the head
commit the AI reported (`--match-head-commit`); this is new, in the spirit
of entry 9's note that any future automation should check the head is the
commit CI passed. There is still no auto-merge.
**Why:** the gate is the decision, not the click. In practice the
maintainer decides in chat and asks Claude to run the merge (#90), which
entry 9 did not describe.
**Considered:** the maintainer clicking every merge (what 9 said, and not
what happens); auto-merge (still not needed at this volume).

## 12. UI screenshots only when the look is the point

**Decided:** a pull request that changes the UI says how to see the change
(`make dev`, the URL, any configuration) and what was checked in a browser.
Screenshots are needed only when the look itself is the point — design
tokens, layout, visual polish — or when the maintainer asks.
**Why:** the AI session cannot upload images to GitHub, and for most UI
changes the checks that matter (CSP, texts in both languages, secrets never
echoed, status codes) are tests and a browser run, not a picture (#90).
**Considered:** a screenshot for every UI change (the earlier rule; it cost
a maintainer round trip on #56 and was not followed afterwards);
screenshots taken by CI as workflow artifacts (no upload problem, but more
tooling; revisit when M5 makes screenshots frequent).

## 13. Sign-up may reveal that an email address is registered

**Decided:** while `RIBBITTO_SIGNUP=on`, public sign-up answers a duplicate
email with "already registered". Anyone can therefore confirm that an
address has an account. The sign-up rate limits only slow that probing
(3 attempts, then one every 10 minutes per client); they do not prevent it.
Sign-in gives unknown emails and wrong passwords the same response, with a
dummy hash to reduce timing differences, and setup has no account-existence
response.
**Why:** M1 has unique email addresses, signs a new account in at once, and
sends no email. Under those requirements there is no honest response that
is the same for new and existing addresses (#39; audit #93, F2).
**Considered:** answering every sign-up with "check your inbox" and telling
an existing owner by email, which needs email delivery (revisit with
invitations or email verification); allowing duplicate unverified
addresses (conflicts with unique email as the sign-in identifier).

## 14. A modular monolith by feature, migrated after M3

**Decided:** ribbitto moves towards a modular monolith organised by
feature. The provisional modules are `identity` (accounts, passwords,
sessions), `org` (organisations, memberships, authorisation), `channel`,
`message` and `realtime`; later unread, presence, files and search. The
shared kernel is the IDs and domain value types, the per-organisation
`event_seq`, and the authorisation entry point. The code migrates after M3,
one module at a time, adding Go `internal` directories and per-module
depguard rules then. Until then, the current layering, the placement of
authorisation in `internal/app` and the depguard rules stay as they are;
new code goes into feature packages inside the layers and follows the
feature map in `docs/architecture/README.md`. Recurring shared-file
conflicts, AIs needing unrelated features to do a task, or repeated
boundary findings in review are triggers to reassess this plan in a
separate issue, not permission to migrate early.
**Why:** in M1 every feature touched the same shared files, which cost
several rebase rounds and caused resolution mistakes, and any change needed
most of the architecture document (#2, #106). ribbitto aims at the scope of
Zulip or Mattermost and is built mostly by AI, so this grows with every
feature. The boundaries are not known yet; M2 and M3 will show them. Moving
packages and rewriting imports is mechanical, but separating shared
transactions, APIs and ownership may need design, which is better done
with that evidence.
**Considered:** migrating now (a big rewrite before the boundaries are
known); staying a layered monolith with feature files inside the layers, as
Zulip and Mattermost do at that scope. That their large layer packages make
AI work with limited context hard is a hypothesis, supported by M1 but not
proven.

## 15. Every ordinary response has a bounded write; ribbitto does not rely on a proxy for it

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

## 16. Email goes through an external SMTP server

**Decided:** ribbitto sends mail through an external SMTP server and runs
no mail server of its own. It speaks standard SMTP, not a vendor API; SES,
Postfix, SendGrid, Mailgun and others are used through SMTP. ribbitto
starts and works without SMTP configured. Enabling a feature that needs
delivery (password reset, email verification, invitation mail) makes the
SMTP settings required, and startup fails without them. A non-sending
Mailer serves development and tests. Until delivery exists, the identity
rules in `docs/domain/README.md` (invariant 8) stand: email addresses are not
verified and prove nothing. The plan and its order are in #128.
**Why:** running a mail server well (deliverability, reputation, abuse
handling) is a product of its own; every self-hoster already has, or can
rent, an SMTP relay. Keeping SMTP optional lets a small instance run with
none, while a feature that cannot work without mail fails loudly instead of
silently.
**Considered:** a built-in mail server (large and hard to operate
safely); a vendor API such as SES or SendGrid (ties self-hosters to one
provider); making SMTP always required (blocks the simplest installs).

## 17. Full English words and stable identifiers in page URLs

**Decided:** page URL segments use full English words; collections are
plural (`organizations`, `channels`, `members`, `messages`). A resource
inside a collection uses its stable identifier, never its display name:
the organisation's `slug`, a channel's UUID. This rule also applies to
later member, message and settings routes. Organisation pages start at
`/organizations/{slug}/`; channel pages will use
`/organizations/{slug}/channels/{channel-id}` (#76). The existing
`/signin`, `/signup` and `/setup` stay as they are; a JSON API's URL scheme
is outside this decision.
**Why:** full words make URLs easier to read and guess; stable identifiers
keep links valid when display names change. ribbitto is unreleased, so the
old `/o/` prefix is removed without redirects (#132).
**Considered:** abbreviated `/o/` and `/c/` segments (less readable);
display names or readable channel slugs (unnecessary naming and rename
rules); redirects from the old prefix (nothing deployed needs them).

## 18. Stored message formats never change meaning

**Decided:** M2 message bodies are plain text, with no `format` column.
Adding Markdown or rich text later must also add an explicit format (for
example `plain_text`, `markdown`, `rich_text_v1`) and mark every existing
message `plain_text`. A stored message is never reinterpreted in a new
format. The stored body is the source of truth; rendered HTML may only be
a derived, disposable cache. The channel list implements this by escaping
stored bodies through templ, preserving line breaks and isolating direction.
**Why:** new rendering features must not change what earlier authors wrote
or turn their literal text into markup (#74).
**Considered:** inferring a format or reinterpreting all history when a
renderer changes (breaks compatibility); storing rendered HTML as the
source (loses the original text).

## 19. Web layers: server-owned HTML, htmx swaps, JavaScript as enhancement

**Decided:** core functionality works without JavaScript; JavaScript is
progressive enhancement. Handlers convert use-case results into view
models, and templ components never receive `app` types (pure `domain`
values are fine). htmx only requests and swaps; the server and templ own
the HTML. Pages are full pages, and M3's SSE units are explicit templ
fragments. JavaScript does not generate HTML, own application state or
make requests. It stores only non-sensitive UX settings, and does
focus, scroll, keyboard, local time and SSE glue. Details, checks and the
audit: [`docs/architecture/web-layers.md`](docs/architecture/web-layers.md).
**Why:** M3 adds SSE fragments and more scripts across exactly these
boundaries; without written rules each change would pick its own (#193).
**Considered:** full JavaScript parity (not needed for enhancements such as
real-time updates); a headless-browser test dependency such as go-rod
(deferred until after M3, if needed); a client-side framework (`DECISIONS.md`
2).

## 21. Topics inside channels, a default topic, and branching instead of threads

**Decided:** every channel message is in exactly one topic. Every channel
has exactly one default topic (UI label *chorus*, #266), created with the
channel, never deleted or replaced, and without a user-defined name; a
message posted without a topic goes there, so nobody has to name a topic to
start talking. A channel opens on the **feed** — all its topics interleaved
by time, each message labelled with its topic — and the **topic view** shows
one topic. **Branching** moves selected messages to another topic of the
same channel, new or existing, in one transaction, and posts a branch notice
in the source topic. Moved messages keep their `id` and original
`event_seq`; the move and the notice are new durable events, so live clients
replay them in order (5). If a selected message is no longer where the
request expected, nothing changes (409). Topics come before M4, whose unread
counts and sidebar build on them. Rules:
[`docs/domain/topics.md`](docs/domain/topics.md).
**Why:** parallel conversations in one channel should not tangle, and
people start talking before they know a conversation deserves its own
place; "talk first, branch later" keeps both (#274).
**Considered:** Slack-style threads (a second place to post and a second
unread model, and replies leave the stream); copying messages into the new
topic (two copies diverge on edit or delete); one message in several topics
(visibility, unread and delete each gain a second meaning); requiring a
topic before posting (people skip it or give up); topics after the MVP
(unread and the sidebar would be built twice).
