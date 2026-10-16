# 41. Client storage: IndexedDB by data class

**Decided** (#1244, approved by the maintainer on 2026-10-17). Adopt option
B: allow IndexedDB by data class when the use and its effect are clear.
Prefer designs that improve UX and perceived speed. This is an exception
route for client storage under
[decision 19](19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)
and replaces [decision 37](37-composer-javascript-pending-posts-keys-and-drafts.md)'s
blanket IndexedDB ban. Each use needs its own issue and the requirements
below.

## Store preference

Prefer the simplest store that is enough, in this order:

1. Memory.
2. The HTTP cache of an immutable, versioned asset
   ([rendering](../architecture/rendering.md)).
3. `sessionStorage` or `localStorage`, only within their current
   [allowances](../architecture/web-javascript.md#javascript): disposable,
   non-sensitive UX settings in `sessionStorage`; decision 37's bounded
   drafts, owner metadata and sign-out notice in `localStorage`, in
   `message-drafts-vN.js` alone. Size does not make other data eligible.
4. IndexedDB, under the data classes below.

This decision does not widen either Web Storage allowance. The server
remains the source of truth. Requests stay with htmx under decision 37 (c),
unless a later decision allows otherwise; storage gives JavaScript no new
request or HTML-generation permission.

## Data classes

The three classes are mutually exclusive. Data takes the class of the most
restrictive data it holds or is derived from. Combining public data with
organisation data makes it class 2; deriving an index from message bodies
makes it class 3. Credentials, session tokens and their hashes are never
stored, in any class.

### 1. Public data or data derived from a public, versioned asset

No organisation data. Examples are emoji names and keywords, or an index
built from such an asset. Allowed in the feature's own issue, with the
light requirements here and the per-use checklist: key it by asset version
and rebuild when it changes. No session binding or session cleanup is
needed. An emoji index's benefit is prospective, to be measured by its issue.

### 2. Organisation data that is not message content

Examples are member handles and names for mention candidates, or channel
names. A member can lose access when removed from the organisation.
Allowed in the feature's own issue only with all of these safeguards:

- Decision 37 (d)'s ownership model: key by member and session, check the
  owner on every read, and give the data an expiry. The session marker is
  not a credential; never use a token or its hash as an owner key.
- Removal at sign-out and cleanup by a **strictly-older horizon**, as in
  decision 37 (d). A stale page must neither remove a newer session's data
  nor make its own data readable under a later member or session.
- A stated way to remove the data once the member's access ends. A
  signed-out or non-member request gets a plain 404 page with no scripts,
  so cleanup waits for a later page load that runs it. Expiry prevents
  reading (restoring) data and makes the next cleanup delete it; it does
  **not** guarantee deletion from disk by that time. With no ribbitto page
  running, nothing is deleted, as decision 37 (d)'s *Limits* says.
- Invalidation by a server version or event sequence. A cached value is
  never trusted over the server and grants no access.
- A security review of the implementing pull request.

### 3. Message content

Bodies, quotes and anything rendered from messages, including a message
cache or offline viewing, need their **own decision**, with a security
review. It must cover invalidation for moves and permission changes,
account switching, and data left after sign-out. Class 2 cannot be used to
store an extract or index of message content.

That decision must respect stable message identity and current location
([decision 36](36-one-message-has-a-stable-url.md)), and frozen format
meaning and server-owned rendering (decisions
[18](18-stored-message-formats-never-change-meaning.md) and
[39](39-message-bodies-use-a-small-frozen-markup.md)). Decision 37's
existing bounded drafts allowance, including inserted quotes, stays as it
is; this class does not authorise a message cache through that allowance.

## Per-use checklist

Every IndexedDB use states in its issue:

- **Purpose and measurements.** Every use requires measurements, kept to
  the minimum its use and impact need. A speed-motivated use measures the
  problem and its effect. A functional use, such as offline viewing, also
  states the function. In every case, including uses not aimed at
  performance, confirm that the related existing metrics on the paths it
  touches do not regress ([the vision's metrics](../vision.md#fast),
  [decision 28](28-the-concept-fast-nothing-gets-buried-nothing-to-learn.md)).
  Measurement work needs approval and a cap under
  [investigations](../workflow/investigations.md). Avoid excessive
  benchmarks or surveys that slow development.
- **Data class**, including the most restrictive source of derived data,
  and the permission or separate decision that class requires.
- **Size bound and quota handling.** A storage error falls back to the
  network and never breaks a function.
- **Schema version and migration.** Dropping and rebuilding is acceptable.
- **Expiry**, including when reading stops and when cleanup can delete the
  data from disk.
- **Cleanup**, including the implementation and tests required below.

## Cleanup today and what each use adds

Today a successful sign-out sends `Clear-Site-Data: "storage"` (decision
37 (d)), which also clears IndexedDB in supporting browsers. The sign-in
page's `web/static/message-drafts-v1.js` cleanup removes only
`ribbitto.draft.v1:` keys in `localStorage`; it does not clean IndexedDB.

Each class 2 or 3 use must extend sign-in and organisation-page cleanup to
its own store, with the ownership and horizon rules above. It must test
stale-tab writes, session expiry and sign-out when the header is
unsupported. A header alone does not address a pending save in another
tab. The drafts requirements are spread across #1178 (sign-out and
sign-in page cleanup, implemented), #1181 (organisation-page cleanup, not
yet implemented) and #1162 (interleaved tabs, owner-checked restore and
sign-out with a pending save, not yet implemented). Each new use supplies
its own protection; these drafts issues do not implement it for IndexedDB.
Class 1 needs no session cleanup.

This decision changes documentation only. The `indexedDB` pattern remains
forbidden in every application script until the first allowed use. That
use's issue makes the check file-scoped, like `localStorage` in
`message-drafts-vN.js`; no ban check or other test changes here.

**Why:** a blanket ban makes public data pay for message data's privacy
and correctness costs. Public assets and their indexes can be rebuilt
without session ownership. Organisation data can outlive access, and
message copies can go stale after moves, permission or format changes.
On a shared device, stored text is plaintext in the browser profile.
Classes keep low-risk uses easy while requiring those costs to be
addressed where they arise. The measurements preserve the vision's rule
against optimising without evidence and show whether a use improves the
experience without regressing existing paths.

**Considered:**

- **A. Keep the ban; each use argues its own decision.** No general rule
  changes, but repeats decision work even for low-risk public data such as
  a possible emoji index.
- **B. Allow by exception under a general rule, by data class (chosen).**
  Public data is easy to use, while organisation data and message content
  keep their safeguards. The rules must be stated and checked per use.
- **C. Allow freely.** Least process, but repeats decision 37's ownership
  and cleanup problems ad hoc and risks message data leaking across
  sessions.
