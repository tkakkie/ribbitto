# Roadmap

Where ribbitto is heading, in coarse steps. No dates: this is a hobby
project. What is happening right now is in the pinned **Status** issue
(#1); individual tasks are issues grouped by GitHub milestone.

Update this file when a milestone is finished or the plan changes, not for
day-to-day progress.

## MVP

| Milestone | Goal | Done when |
|---|---|---|
| **M0 Foundation** ← now | Skeleton, lint with enforced layering, PostgreSQL with migrations and sqlc, templ + htmx + Tailwind hello page, architecture and domain docs, i18n (English and Japanese), UI mock and design tokens | `make check` and CI pass; design tokens chosen |
| **M1 Accounts** | First-run setup, sign-up, sign-in, sign-out, server-side sessions | Security checklist met: CSRF, cookie attributes, hashed session tokens, rate limits, one-time setup |
| **M2 Channels and messages** | Public channels, posting, history with paging | Two people can talk (after a reload) |
| **M3 Real time** | Server-Sent Events hub with per-connection authorization and replay | Messages arrive instantly; nothing is lost on reconnect; nothing reaches a connection that may not read it |
| **M4 Awareness** | Unread counts, presence, typing indicator | |
| **M5 Polish** | Dark mode, mobile layout, motion | |

## After the MVP

Roughly in this order: private channels → direct messages → invitations →
reactions → editing and deleting → PostgreSQL row-level security →
multiple organisations → file uploads → search.

Self-hosting (container image on GHCR, Compose file with PostgreSQL and
Caddy) becomes a release goal once M3 works.

## Not planned

Native mobile apps, federation, voice and video. These may be revisited,
but nothing in the design should be bent for them now.
