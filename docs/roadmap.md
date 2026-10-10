# Roadmap

Where ribbitto is heading, in coarse steps. No dates: this is a hobby
project. What is happening right now is in the pinned **Status** issue
(#1); individual tasks are issues grouped by GitHub milestone.

Update this file when a milestone is finished or the plan changes, not for
day-to-day progress. What the steps aim at is in the [vision](vision.md).

## MVP

| Milestone | Goal | Done when |
|---|---|---|
| **M0 Foundation** ✓ done | Skeleton, lint with enforced layering, PostgreSQL with migrations and sqlc, templ + htmx + Tailwind hello page, architecture and domain docs, i18n (English and Japanese), UI mock and design tokens — plus the AI workflow: templates, cross-review, closure verification, Grok adversarial review | `make check` and CI pass; design tokens chosen |
| **M1 Accounts** ✓ done | First-run setup, sign-up, sign-in, sign-out, server-side sessions | Security checklist met: CSRF, cookie attributes, hashed session tokens, rate limits, one-time setup |
| **M2 Channels and messages** ✓ done | Public channels, posting, history with paging | Two people can talk (after a reload) |
| **M3 Real time** ✓ done | Server-Sent Events hub with per-connection authorization and replay | Messages arrive instantly; nothing is lost on reconnect; nothing reaches a connection that may not read it |
| **Topics** ✓ done | Topics inside channels: the default topic, the feed and topic view, branching ([decision 21](decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)) | A conversation started in the default topic can be branched into its own topic, and both views show it |
| **M4 Awareness** ← next | Unread counts, presence, typing indicator | The four outcomes under [M4](#m4) below |
| **M5 UI pass** | The existing pages in a new visual direction, with icons, channel images and motion | The five outcomes under [M5](#m5) below |
| **M6 Dark mode and mobile** | Dark colours and a mobile layout for M5's design | The three outcomes under [M6](#m6) below |

The modular-monolith migration ([decision 26](decisions/26-modules-by-feature-layout-seams-and-order.md),
[decision 27](decisions/27-channels-topics-and-messages-are-one-conversation-module.md),
[`modules.md`](architecture/modules.md)) is done: `identity`, `realtime`,
`org` and `conversation` are modules, and the old layers are gone. The
load tests before M4 are done (#216, #219; [results](architecture/load-results.md)).

## M4

M4 is done when a member of an organisation sees:

1. **Unread counts, per topic.** The sidebar shows how many messages the
   member has not read in each channel and in each topic it links. The
   counts follow new messages and reading without a reload, in all of the
   member's tabs. Reading one topic never marks another topic's messages
   read, and branching never changes whether a message is read
   ([`unread.md`](domain/unread.md#topics)).
2. **Where they left off.** Opening a channel or a topic with unread
   messages shows a divider above the first one when it is on the loaded
   page, and it stays put while the member reads.
3. **Who is online.** The channel's members panel marks the members who
   have ribbitto open, and updates live.
4. **Who is typing.** The channel or topic the member is reading shows who
   is typing there; the indicator goes away soon after they stop or send.

Each signal reaches only members who may see it, and a reconnect restores
the current state. How they reach the browser is in decisions
[30](decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md) and
[31](decisions/31-presence-and-typing-are-current-state-with-a-generation.md).

Left for later: notifications, sounds and a count in the tab title;
jumping to the first unread message; away, idle and "last seen"; typing in
reply chains; new channels appearing live in the sidebar (#162); the "for
me" view (#433); presence across several server processes (#236).

## M5

M5 starts once M4 is done. It gives the pages that exist today one design,
for a desktop browser: white-based, with a light sidebar of icon-and-label
rows, a wide reading column and a rounded composer, and on desktop the feel
of Microsoft's public website. It replaces the "Clear water" direction and
its colours from #9. M5 is done when:

1. **The direction is written down.** [`ui.md`](ui.md) records the new
   direction, its tokens with colours chosen anew, and the contrast table;
   it also says how presentation and placement make the frog labels'
   meaning clear while they stay names
   ([vision](vision.md#nothing-to-learn), decisions
   [20](decisions/20-product-labels-and-ordinary-words.md) and
   [28](decisions/28-the-concept-fast-nothing-gets-buried-nothing-to-learn.md)).
2. **Every existing page follows it:** setup, sign-in, sign-up, the
   channel feed, the topic view, the members pages and the error pages.
3. **Icons come from one SVG set** kept in the repository with its
   licence, never from a CDN.
4. **Channels have images.** A channel shows an image instead of `#` where
   channels are listed and in its header.
5. **Motion is small and optional.** It uses shared duration and easing
   tokens, animates only properties that are cheap to render, and is off
   under `prefers-reduced-motion`.

The performance rules still hold: images never delay a message's text or
shift the layout ([`ui.md`](ui.md#planned-directions-from-the-maintainer-9)),
and a change on a measured path states its effect and any known regression
([vision](vision.md#fast)).

Left for later: dark mode and the mobile layout (M6); the rich editor and
the emoji picker.

## M6

M6 is done when, in M5's design:

1. **Dark mode.** Every page has dark colours that follow the system
   setting and meet the contrast table, checked in a browser.
2. **Mobile layout.** At phone width, every page is one column, with the
   conversation at full width and a bottom navigation; tap targets are at
   least 44 × 44 px and nothing scrolls sideways.
3. **Motion** behaves the same in dark mode and on a phone.

Left for later: a manual light/dark switch.

## After the MVP

ribbitto is a hobby project. The repository is public, but a release — a
container image and a Compose file with PostgreSQL and Caddy
([decision 7](decisions/07-rental-vps-and-containers.md)) — comes later,
when the maintainer chooses, once more is built. Until then the code,
security and documents stay ready to release.

The [vision](vision.md)'s principles stay. The technical constraints set to
finish the MVP may change after M6 where a need is shown, each through a new
decision that records why and what changes, never by quietly breaking an
old one. Roughly in this order:

1. **Revisit the constraints (short).** Measure in the browser, starting in
   M5: input latency, INP, switching, initial JavaScript, memory. Then
   decide, each in its own issue and on measurements or a prototype: the
   front end's roles, including whether Svelte islands serve client-state UI
   such as the composer (decisions
   [2](decisions/02-server-rendered-html-with-htmx-not-an-spa.md) and
   [19](decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)),
   tried first in one composer prototype with a size and INP budget; what
   browser storage may hold, by use
   ([web layers](architecture/web-layers.md#javascript)); a message format
   and safe Markdown rendering
   ([decision 18](decisions/18-stored-message-formats-never-change-meaning.md),
   no `templ.Raw`); the URL of one message. Nothing here is adopted until
   its decision is made.
2. **Two tracks in parallel.** The composer track never blocks the basics.
   - *Basics:* private channels → direct messages → email (Mailer →
     password reset → email verification) → invitations (#128).
   - *Composer:* reliable sending (no duplicate posts, the text kept when
     sending fails, the sending state shown, and the post shown before the
     server answers if step 1's decisions allow it) → an input that grows and a
     send-key setting → mentions (#117) → links to one message → quoting,
     including a selection → drafts (#88, revisited after step 1).
3. **Messages:** reactions, editing and deleting, reply chains
   ([decision 22](decisions/22-replies-stay-in-the-stream-with-a-reply-chain-panel.md)),
   file uploads.
4. **Topics:** resolving a topic with its conclusion, following and muting
   topics, an inbox of unread topics with keyboard navigation and jumping to
   the first unread message, the "for me" view (#433, once mentions and
   replies exist), starting a topic from a quoted selection (unlike
   branching, it moves no message), search that handles Japanese, a quick
   switcher.
5. **Integrations and operations,** mostly for a release: webhooks and bots
   that post into topics, Web Push, import and export, deactivating
   members, PostgreSQL row-level security, multiple organisations.

The rule for [vanished messages](architecture/replay.md#vanished-messages)
is settled (#352); the first feature that deletes a message or moves one to
another channel implements it.

Email delivery and email verification are not part of the MVP (accounts
still sign in with an email address). Delivery arrives with the basics track
in step 2, starting with a Mailer over SMTP
([decision 16](decisions/16-email-goes-through-an-external-smtp-server.md));
#128 holds the plan and its requirements.

Bulk onboarding of an organisation's people goes through invitations and
provisioning, never through the public `/signup` form.

## Not planned

Native mobile apps, federation, voice and video. These may be revisited,
but nothing in the design should be bent for them now.

Not planned for now either: turning the front end into a Svelte
single-page app (the message list, sidebar and counts stay server-rendered),
caching messages in IndexedDB for offline reading, read receipts per
message, and tasks.
