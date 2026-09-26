# Decisions

Decisions that later work must respect, newest last. Each entry is short:
what was decided, why, and what else was considered. To change one, open an
issue; when it is settled, add a new entry that supersedes the old one
rather than editing history.

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
