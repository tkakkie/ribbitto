# 28. The concept: fast, nothing gets buried, nothing to learn

**Decided:** ribbitto aims to be a team chat that is fast, where nothing
gets buried, and with nothing to learn; [`docs/vision.md`](../vision.md)
spells out each principle. *Fast* is two measured goals: perceived speed in
the browser, and server capacity on a reference machine of 2 vCPU and 4 GB
running the application, PostgreSQL and Caddy (7). Decisions 2 and 19
stand: speed comes from measuring and fixing the bottleneck, and a
client-side change that would alter them comes through an issue with
measurements. Unread state and notifications work per topic (21); a
"for me" view, never a channel, lists mentions of and replies to a person;
defaults work without settings. Every channel keeps one default topic (21),
and frog labels stay names (20) whose meaning comes from presentation and
placement, not from ordinary words added beside them. Decisions 2, 19, 20
and 21 are unchanged.
**Why:** the README said what ribbitto is, not why anyone would pick it;
the maintainer's concept gives issues and reviews a shared tie-breaker
before M4 settles unread state and the stream's scope (#282, #296) (#432).
**Considered:** a "for me" channel (people would ask whether they can post
there and who its members are); ordinary words beside every frog label
(longer labels everywhere); prefetching, browser-side HTML or Svelte now
(no measurement yet shows they are needed); one speed goal (what people
wait for and what a server carries are measured differently).
