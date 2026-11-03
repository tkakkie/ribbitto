# MVP milestones: M4, M5 and M6

Outcomes and notes for the finished [MVP](roadmap.md#mvp) milestones M4, M5
and M6 in the [roadmap](roadmap.md).

## M4

M4 is done: unread counts per channel and topic that follow
posts, moves and reads in all of a member's tabs (#286, #711), the unread
divider (#291), presence on the members panel (#287) and the typing
indicator (#288). Outcomes:

1. **Unread counts, per topic.** The sidebar shows how many messages the
   member has not read in each channel and linked topic. The counts follow
   new messages and reading without a reload, across the member's tabs.
   Reading one topic never marks another topic's messages read, and
   branching never changes whether a message is read
   ([`unread.md`](domain/unread.md#topics)).
2. **Where they left off.** Opening a channel or topic with unread
   messages shows a divider above the first one when it is on the loaded
   page, and it stays put while the member reads.
3. **Who is online.** The channel's members panel marks members who
   have ribbitto open, updating live.
4. **Who is typing.** The channel or topic being read shows who is typing
   there; the indicator clears soon after they stop or send.

Each signal reaches only members who may see it, and a reconnect restores
the current state. How they reach the browser is in decisions
[30](decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md) and
[31](decisions/31-presence-and-typing-are-current-state-with-a-generation.md).

Left for later: notifications, sounds and a count in the tab title;
jumping to the first unread message; away, idle and "last seen"; typing in
reply chains; new channels appearing live in the sidebar (#162); the "for
me" view (#433); presence across several server processes (#236).

## M5

M5 is done (#870–#879; whole-milestone review #881). It gave existing pages one desktop
design: white rounded blocks on a lightly tinted base — an app bar, the
conversation and side panels — with the sidebar of icon-and-label rows on
the base, a wide reading column and a rounded composer, and on desktop the
feel of Microsoft's Fluent 2. It replaced the "Clear water" direction and
its colours from #9. M5 shipped light pages and removed the old dark theme;
M6 added dark colours for the new direction (#967). Outcomes:

1. **The direction is written down.** [`ui.md`](ui.md) records the new
   direction, its tokens with colours chosen anew, and the contrast table,
   and how presentation and placement make the frog labels'
   meaning clear while they stay names
   ([vision](vision.md#nothing-to-learn), decisions
   [20](decisions/20-product-labels-and-ordinary-words.md) and
   [28](decisions/28-the-concept-fast-nothing-gets-buried-nothing-to-learn.md)).
2. **Every existing page follows it:** setup, sign-in, sign-up, the
   channel feed, the topic view, the members pages and the existing error
   page (too many attempts). 404 and 500 stay plain text; M5 adds
   no new error pages.
3. **Icons come from one SVG set** kept in the repository with its
   licence, never from a CDN.
4. **Channels have images.** Channels show an image instead of `#` in lists
   and headers.
5. **Motion is small and optional.** It uses shared duration and easing
   tokens, animates only properties that are cheap to render, and is off
   under `prefers-reduced-motion`.

The performance rules still hold: images never delay a message's text or
shift the layout ([`ui.md`](ui.md#planned-directions-from-the-maintainer-9)),
and a change on a measured path states its effect and any known regression
([vision](vision.md#fast)). #881 measured M5 against the prior commit:
switching was 42 ms vs 28 ms, from cross-document View Transitions, so
#921 replaced them with CSS arrival motion (34–36 ms). Posting was
unchanged. The Claude and Codex audit found no serious security issue;
follow-ups (#936–#940) are merged.

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
