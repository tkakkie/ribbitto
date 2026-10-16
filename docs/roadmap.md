# Roadmap

A coarse plan without dates: ribbitto is a hobby project. Current work is
in the pinned **Status** issue (#1); tasks are issues grouped by GitHub
milestone.

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
| **M4 Awareness** ✓ done | Unread counts, presence, typing indicator | The four outcomes under [M4](#m4) below |
| **M5 UI pass** ✓ done | The existing pages in a new visual direction, with icons, channel images and motion | The five outcomes under [M5](#m5) below |
| **M6 Dark mode and mobile** ✓ done | Dark colours and a mobile layout for M5's design | The three outcomes under [M6](#m6) below |

The modular-monolith migration ([decision 26](decisions/26-modules-by-feature-layout-seams-and-order.md),
[decision 27](decisions/27-channels-topics-and-messages-are-one-conversation-module.md),
[`modules.md`](architecture/modules.md)) is done: `identity`, `realtime`,
`org` and `conversation` are modules, and the old layers are gone. The
load tests before M4 are done (#216, #219; [results](architecture/load-results.md)).

## M4

M4 is done: unread counts per channel and topic that follow
posts, moves and reads in all of a member's tabs (#286, #711), the unread
divider (#291), presence on the members panel (#287) and the typing
indicator (#288). The completed outcomes are:

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

M5 is done (#870–#879; whole-milestone review #881). It gave the pages
that existed then one design, for a desktop browser: white rounded blocks
on a lightly tinted base — an app bar, the conversation and side panels —
with the sidebar of
icon-and-label rows on the base, a wide reading column and a rounded
composer, and on desktop the feel of Microsoft's Fluent 2. It replaced the
"Clear water" direction and its colours from #9. M5 shipped light pages:
it removed the old dark theme; M6 added dark colours for the new direction
(#967).
The completed outcomes are:

1. **The direction is written down.** [`ui.md`](ui.md) records the new
   direction, its tokens with colours chosen anew, and the contrast table;
   it also says how presentation and placement make the frog labels'
   meaning clear while they stay names
   ([vision](vision.md#nothing-to-learn), decisions
   [20](decisions/20-product-labels-and-ordinary-words.md) and
   [28](decisions/28-the-concept-fast-nothing-gets-buried-nothing-to-learn.md)).
2. **Every existing page follows it:** setup, sign-in, sign-up, the
   channel feed, the topic view, the members pages and the existing error
   page (too many attempts). 404 and 500 stay plain-text responses; M5 adds
   no new error pages.
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
([vision](vision.md#fast)). #881 measured M5 against the commit before it:
switching was 42 ms against 28 ms, from cross-document View Transitions, so
#921 replaced them with CSS arrival motion (34–36 ms). Posting was
unchanged. A Claude and Codex audit of M5 found no serious security issue;
its follow-ups (#936–#940) are merged.

Left for later: the rich editor and the emoji picker.

## M6

M6 is done (review #976), in M5's design:

1. **Dark mode.** Every page ([listed in `ui.md`](ui.md#layout-principles);
   404 and 500 stay plain text) follows the system setting with dark
   colours that meet the contrast table, checked in a browser.
2. **Mobile layout.** At phone width, every page is one column, with the
   conversation at full width, a bottom navigation on the channel list and
   a back link in a conversation; tap targets are at least 44 × 44 px and
   nothing scrolls sideways.
3. **Motion** behaves the same in dark mode and on a phone.

Left for later: a manual light/dark switch.

## After the MVP

The repository is public, but a release — a
container image and a Compose file with PostgreSQL and Caddy
([decision 7](decisions/07-rental-vps-and-containers.md)) — comes later,
when the maintainer chooses, once more is built. Until then the code,
security and documents stay ready to release.

The [vision](vision.md)'s principles stay. The technical constraints set to
finish the MVP may change after M6 where a need is shown, each through a new
decision that records why and what changes, never by quietly breaking an
old one. Roughly in this order:

1. **Revisit the constraints (short).** Measure in the browser: input
   latency, INP, switching, initial JavaScript, memory. Then
   decide, each in its own issue and on measurements or a prototype: the
   front end's roles, set by
   [decision 40](decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md); composer
   state and browser storage, set by
   [decision 37](decisions/37-composer-javascript-pending-posts-keys-and-drafts.md);
   a body format and safe rendering, set by
   [decision 39](decisions/39-message-bodies-use-a-small-frozen-markup.md);
   the URL of one message, set by
   [decision 36](decisions/36-one-message-has-a-stable-url.md). The rest
   waits for its decision.
2. **Two tracks in parallel.** The composer track never blocks the basics.
   - *Basics:* private channels → direct messages → email (Mailer →
     password reset → email verification) → invitations (#128).
   - *Composer:* reliable sending (no duplicate posts, the text kept when
     sending fails, the sending state and the post shown before the
     server answers) → an input that grows and a send-key setting →
     mentions (#117) → links to one message → quoting, including a
     selection → device drafts (#1162; #88 cross-device).
3. **Messages:** reactions, editing and deleting, [pins](domain/pins.md)
   (#880; rules agreed, implementation after M6), reply chains
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
offline message caching ([decision 41, class 3](decisions/41-client-storage-indexeddb-by-data-class.md#3-message-content)), read receipts per
message, and tasks.
