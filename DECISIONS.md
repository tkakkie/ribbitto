# Decisions

Decisions that later work must respect, newest last. Each entry is short:
what was decided, why, and what else was considered.

**Keep it current:** add an entry in the same pull request whenever a
long-lived choice is made that later work must follow: a technology, a data
model, a product or a maintenance policy, or a repository-wide policy that
changes or supersedes an existing decision. To change a decision, open an
issue; when it is settled, add a new entry that supersedes the old one
rather than editing history.

**Workflow rules are not decisions** (#363). Operating procedures, review
gates and the rules for running AI tools live in
[`docs/workflow/`](docs/workflow/README.md), which is their authoritative
place. They are added or changed from a `process` issue, without a
decision entry. The existing process decisions stay as history and are not
reorganised. A workflow change that conflicts with an existing decision
needs a new decision that supersedes it.

## Adding a decision

Choose the next free number after the last entry and create one file in
`docs/decisions/`, named `NN-slug.md` (two digits, lowercase words or digits
separated by hyphens). Start it with `# N. Title` and add one index line
below: `- [N. Title](docs/decisions/NN-slug.md)`. Keep existing numbers.
`make check` enforces the file, heading and index-number format, the index's
coverage and the normal document size limit for both the index and entries.

## Index

- [1. Go, not Rust](docs/decisions/01-go-not-rust.md)
- [2. Server-rendered HTML with htmx, not an SPA](docs/decisions/02-server-rendered-html-with-htmx-not-an-spa.md)
- [3. Server-Sent Events plus POST, not WebSockets](docs/decisions/03-server-sent-events-plus-post-not-websockets.md)
- [4. PostgreSQL with sqlc and goose](docs/decisions/04-postgresql-with-sqlc-and-goose.md)
- [5. One event sequence per organisation](docs/decisions/05-one-event-sequence-per-organisation.md)
- [6. Password and session-token hashing](docs/decisions/06-password-and-session-token-hashing.md)
- [7. Rental VPS and containers](docs/decisions/07-rental-vps-and-containers.md)
- [8. Languages](docs/decisions/08-languages.md)
- [9. Manual merges](docs/decisions/09-manual-merges.md)
- [10. Argon2id parameters and a cap on concurrent hashing](docs/decisions/10-argon2id-parameters-and-a-cap-on-concurrent-hashing.md)
- [11. The maintainer decides merges; an AI may execute them](docs/decisions/11-the-maintainer-decides-merges-an-ai-may-execute-them.md)
- [12. UI screenshots only when the look is the point](docs/decisions/12-ui-screenshots-only-when-the-look-is-the-point.md)
- [13. Sign-up may reveal that an email address is registered](docs/decisions/13-sign-up-may-reveal-that-an-email-address-is-registered.md)
- [14. A modular monolith by feature, migrated after M3](docs/decisions/14-a-modular-monolith-by-feature-migrated-after-m3.md)
- [15. Every ordinary response has a bounded write; ribbitto does not rely on a proxy for it](docs/decisions/15-every-ordinary-response-has-a-bounded-write-ribbitto-does-not-rely-on-a-proxy-for-it.md)
- [16. Email goes through an external SMTP server](docs/decisions/16-email-goes-through-an-external-smtp-server.md)
- [17. Full English words and stable identifiers in page URLs](docs/decisions/17-full-english-words-and-stable-identifiers-in-page-urls.md)
- [18. Stored message formats never change meaning](docs/decisions/18-stored-message-formats-never-change-meaning.md)
- [19. Web layers: server-owned HTML, htmx swaps, JavaScript as enhancement](docs/decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)
- [20. Product labels and ordinary words](docs/decisions/20-product-labels-and-ordinary-words.md)
- [21. Topics inside channels, a default topic, and branching instead of threads](docs/decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)
- [22. Replies stay in the stream, with a reply-chain panel](docs/decisions/22-replies-stay-in-the-stream-with-a-reply-chain-panel.md)
- [23. The event log is the source of truth; the hub carries a level](docs/decisions/23-the-event-log-is-the-source-of-truth-the-hub-carries-a-level.md)
- [24. A cursor that cannot be served gets `reset`, never a partial replay](docs/decisions/24-a-cursor-that-cannot-be-served-gets-reset.md)
- [25. Production serves event streams over HTTP/2](docs/decisions/25-production-serves-streams-over-http2.md)
