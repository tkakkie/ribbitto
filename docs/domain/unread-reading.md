# Unread reading

The reading scopes, posting and automatic reading rules from [unread rules](unread.md) are collected here.

## Advancing it

Branch-notice reads, visible-page reading POSTs and posting writes are current.
Only a POST advances read state; a GET never changes it
([routes](../architecture/routes.md)). Each reading POST carries its
scope and `S`, the newest durable sequence the page has applied and shown while
visible. The body names one run with `cursor=S&from=F&through=T`, or the whole scope
with `cursor=S&scope=all` (*Mark all as read*). A run reads only messages
with `F ≤ event_seq ≤ T ≤ S`; a topic run excludes moves after `S` and leaves
messages moved in below the run unread, unlike a whole-scope read (#775).
The whole-scope forms read older history too:

- **Feed:** every message of the channel with `event_seq ≤ S` becomes read.
- **Topic view:** every message of that topic with `event_seq ≤ S` becomes
  read, except one moved in by a move after `S` (`moved_event_seq > S`): the
  page's cursor had not reached that move. A message moved in below the
  loaded window is read by the next topic read whose `S` covers the move,
  like the older history an initial reading POST reads (decided 2026-10-10,
  #775). Other topics are untouched.

Posting reads only the author's own message in its transaction, leaving every
other message's read state and topic floors unchanged. A member's own messages
are never unread for them. Branching already adds its author's notice
as `[p + 1, m + 1)` in its transaction, merged with the persisted join prefix;
the organisation lock keeps the notice newest in its channel. Moving existing
messages changes no member's read state. A sequence
the page has only received, or the post's own response, never counts as
applied.

`S` is the page's snapshot cursor for the POST sent after the page loads,
and later the newest durable sequence the visible page has applied, covering posts
and moves. An application while hidden counts only once the page becomes visible.
At most one reading POST is in flight; afterwards it sends the newest shown
cursor. Hidden tabs, `?before=` pages and the channel list page send no reading POST.
A `?around=` page, opened from a message's URL
([decision 36](../decisions/36-one-message-has-a-stable-url.md)), counts as an
older page here: no reading POST, and a post from it carries no cursor.
A cursor above the organisation's committed `event_seq` is refused, so a
later message is never read in advance. A message that is still unread
when the POST runs, but has moved out of the topic since the page showed
it, stays unread: it is no longer there to mark. A message already read
stays read whatever moves. Without JavaScript, nothing advances on load; the feed and topic view
offer a *Mark all as read* form that sends `cursor` and `scope=all`.
Its button shows only when the page's snapshot found something unread in its
scope (the [divider's](unread.md#the-unread-divider) first unread is non-zero) and,
like the divider, keeps that answer for the visit: live messages are read
automatically once shown, so they never add it. Automatic reading posts
through the same form, which stays on the page, hidden, when nothing was
unread.

**A modal panel pauses automatic reading** (#972). While the members or
overview panel covers the conversation as a modal — a bottom sheet below
852 px, an overlay from 852 to 1200 px — no automatic reading POST is sent:
not on the panel page's load and not for live arrivals, re-evaluated
whenever the width changes with the panel open. The stream is still applied
behind it: new messages, counts and typing appear, but nothing shown then
counts as read. When the panel closes (a page load) or stops covering (the
window widens to 1200 px), automatic reading resumes under the rules above. *Mark all as read* is deliberate and never paused: below 1200 px
the overview offers it, submitting the same form. One click reads up to the
newest shown sequence, and a refused (429) click retries with that cursor
while the panel stays modal; it reads nothing that arrived after the click.

Cursor-only POSTs keep today's whole-scope meaning through `Reading.Feed` or
`Topic`, with no counter or log line here. Today's script still sends them;
run sending and the injected counter/reporter belong to S3a. S5 refuses them
no earlier than 7 and no later than 30 days after S3a deploys. That clock is
not proof old tabs are gone: ordinary tabs can reconnect across deploys,
and the old script does not reload after a non-429 failure. S3a/S5 must decide
recovery and a reporter with start and daily-zero lines (amendment E).

Before S3a merges, S1c-3 can be reverted alone, in reverse dependency order;
new buttons against the old server have their `scope` ignored and still read
the whole scope. After S3a merges, S1c is permanent until S5: an older handler
would turn every run into a whole-scope read, irreversibly growing read state.
Revert S3a first; never revert or downgrade S1c alone. The fail-closed `bound`
key is a decision for S3a, requiring no change here.
