# Vision

ribbitto is a team chat that is **fast, where nothing gets buried, and
with nothing to learn**. This file says what each of those means, what it
rules out, and how a change is checked against it, so issues and reviews
share one tie-breaker. The concept is recorded in
[decision 28](decisions/28-the-concept-fast-nothing-gets-buried-nothing-to-learn.md).

**Changing it:** a change to a recorded choice below needs an issue and a
decision that supersedes decision 28 ([`DECISIONS.md`](../DECISIONS.md));
editorial clarifications go through the ordinary issue and pull request
workflow.

## Fast

Fast is two goals, measured separately: how fast ribbitto feels to the
people using it, and how much a small server can carry. The metrics are
named here; their numbers are added after the first end-to-end load test
(#216) gives a baseline.

**Perceived speed** — time a person waits, measured in the browser:

- *Switch:* from selecting a channel or topic to its messages being shown.
- *Own post:* from sending a message to seeing it in one's own view.
- *Others' post:* from sending a message to its appearing on another
  member's screen (post to receipt, end to end through the stream).
- *First load:* from opening a channel page to its messages being shown.

**Server capacity** — what one process sustains on the reference machine:

- concurrent streams at a fixed posting rate, with every event delivered;
- posts per second at a fixed number of open streams.

The **reference machine** is the class a small team rents to self-host a
chat: **2 vCPU and 4 GB of memory**, running the application, PostgreSQL and
Caddy on one host, as in [decision 7](decisions/07-rental-vps-and-containers.md)'s
Compose setup. The exact machine is named when a run on it happens. #216
runs on a disposable machine with the load generator on the same host, so
its numbers are a baseline, not the reference-machine figure.

**Approach:** keep server-rendered HTML with htmx
([decision 2](decisions/02-server-rendered-html-with-htmx-not-an-spa.md))
and its web layers ([decision 19](decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md));
measure, find the bottleneck, and fix that. A client-side change that would
alter those decisions — prefetching, HTML kept in the browser, Svelte
islands — comes through an issue with the measurements that justify it.

**Rules out:** optimising without a measurement that shows the problem;
adopting a client framework to make the UI feel faster before the
metrics show where time goes.

**Check:** a change on a measured path states its effect on the metric
when it can be measured, and calls out a known regression.

## Nothing gets buried

What needs a person reaches them, and what does not stays out of their way.

- **Topic granularity:** unread state and notifications work per topic, not
  only per channel ([decision 21](decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md);
  the unread model is chosen in #282).
- **A "for me" view:** mentions of a person and replies to their messages
  are listed in one place (#433). It is a **view, not a channel**: nothing
  is posted there and it has no members.
- **Defaults work without settings.** Settings are an escape hatch, never a
  step needed before ribbitto is useful.

**Rules out:** a "for me" channel; a feature that only helps after
configuration; a second place to talk with its own unread model, such as
Slack-style threads ([decision 22](decisions/22-replies-stay-in-the-stream-with-a-reply-chain-panel.md)).

**Check:** a change that adds a source of unread state or notifications
says where it surfaces and what its default is.

## Nothing to learn

A new member can talk before learning how ribbitto is organised.

- **Talk first, branch later:** every channel has one default topic, so
  nobody has to name a topic to start; a conversation that deserves its
  own topic is branched later ([decision 21](decisions/21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)).
- **No premature choices:** the UI never asks people for a decision they
  cannot yet answer.
- **Frog labels stay names** ([decision 20](decisions/20-product-labels-and-ordinary-words.md)).
  Their meaning is made clear by presentation and placement, not by
  ordinary words added beside them; how is settled in the M5 design pass.

**Rules out:** requiring a topic before posting; a new concept that has
to be explained before it can be used; themed words for operations
(decision 20 already rules these out).

**Check:** a change that adds a concept, a label or a choice says who has
to learn it, and when they meet it.

## When principles conflict

An issue or pull request that trades one principle against another says so
and why. For example, a notification setting can help nothing get buried
while adding something to learn.
