# Web composer

## Composer

Bounded state follows [decision 37][d37]: pending own posts (a), idempotency
keys (b), htmx-only requests (c), device drafts (d) and the server-stored send-key
preference (e); *planned* parts await issues.

Idle feed/topic sends use the ordinary path for blank/quoted text, >4,000
code points after newline normalisation/trim, or no `crypto.randomUUID()`.

- **Ordinary sending:** the textarea stays usable in flight; answers replace
  only `#message-composer-state`. A 200 removes the sent request body only if
  it is still a textarea prefix, preserving the remaining caret/selection,
  without input or typing signals. Removal waits for composition to end and
  retains confirmation identity until then. `data-invalid` becomes textarea
  `aria-invalid`.
- **Ordinary failures** keep text and key; `#message-send-alert` distinguishes
  outcomes. 400/404/413/422 reject before any write; 429 means *not sent*, or,
  with `data-unconfirmed-key`, *this retry was refused and the earlier send
  is still unconfirmed*. Network errors, timeouts, 5xx or other 4xx (except
  409) may follow a commit: *could not confirm*, marking the key in
  `data-unconfirmed-key`. Only 200/409 follow key lookup; both renew the key.
  The mark clears exactly on key change.
- **409 proof:** `data-sent-body` suppresses the error only in the same channel,
  with neither stored nor retried text quoted, when stored typed-body byte
  length and SHA-256 digest verify the retry prefix without resolving handles.
  That prefix is removed under the ordinary-send rules: *the earlier message
  was sent*. If the start changed, show the 409 error. Otherwise text/error
  stay, including legacy rows without proof.
- **Stream reset:** pending/unconfirmed sends, an in-flight composer request or
  a failure alert (Not sent/Could not confirm/Still unconfirmed) hold the
  [reload guard](web-javascript.md#javascript). Dispatched requests time out at
  30 s; queues/retries/failed text can extend the hold. Edit's restored text is
  a draft, which does not hold reload.
- **Quote chip:** `#composer-quote-description`: author/excerpt or labelled
  editable part, empty without quote. Quote closes popover; whole swaps focus
  Remove (Tab → Quote only part); parts `#quote-text` (Tab → Remove → body → send);
  removal focuses body.
  422/409 keep focus/caret/selection in server text, matching IDs/button purposes.
  Script owns swaps, suppressing htmx autofocus replay; native autofocus serves
  plain part loads. Chip/gap reduce [growth cap](../ui-composer.md); resizing
  keeps the reader's position. Plain Quote/Remove GET `#message-composer`;
  Remove loses text.
- **Mention picker:** all composers insert typed `@handle` text. Token starts
  mirror posting: line start, lone column-zero `>`, Unicode space or `(`;
  no escapes, URLs or email. Code-span/fence checks are conservative.
  Only matching `data-query` responses open; picks recheck the token.
  Empty results hide the picker; `#mention-status` announces no members.
  Textarea focus stays, `aria-activedescendant` names the option; options have
  44 px targets below `desk`. Plain Enter/Tab pick; modified keys pass through
  except Ctrl/Cmd+Enter closes and follows the send setting. Blur forgets
  dismissal; refocus searches. Composition hides it; Enter has a 100 ms guard
  after commit. Picks dispatch input for drafts and typing.

### Drafts

Only `web/src/message-drafts.ts` may use `localStorage` (checked), for latest
feed/topic composers, never history. [Decision 37 (d)][draft-policy] is canonical
for the member/session/channel/place key and JSON value, restore ownership and
expiry checks, 4,000-code-point/50-entry/7-day bounds, plaintext privacy cost,
cleanup horizons, guaranteed cleanup events and qualifications throughout (d).
An earlier session's text must never appear under a later session or another
member. Send keys restore only for the current member/session/place and
unchanged valid text; edits or emptying renew them.
Value timestamps are Unix milliseconds; `key` is a send UUID or null, and older
entries may omit it.

- **Restore and sending:** restore seeds edit time without input, typing or
  focus; server text wins. [Events](web-javascript.md#javascript): Edit saves;
  Remove clears only its unchanged snapshot. Snapshot keys survive 409
  rotation. Save raw text and send key at dispatch, before the request leaves
  or textarea empties, including in-flight reload/navigation. A 200 or
  stored-key confirmation clears the saved key while keeping newer text.
  Only the latest send dispatched in a place is kept: an unconfirmed item
  followed by another send loses its key on reload, as its text did before.
  Retry keeps unsaved edits; in-place 409 confirms recovered prefixes; dirty
  cleanup keeps edit time/debounce. Lost replies can leave drafts.
- **Saving:** tab-monotonic edit times; debounce and lifecycle flush wait for
  composition to end; clean tabs never flush. Blank/over-limit or confirmed
  blank text writes empty tombstones at edit time, never deletes the key.
  Tombstones restore no text, count toward the cap and persist until eviction,
  session end or retention cleanup. Equal/later stored edits win; skipped
  writes leave the tab clean. Own entries trim by edit time then key, best
  effort without reading or writing foreign keys. Overlapping writes can keep
  older text; eviction can remove a concurrent update. Neither race is repaired;
  textarea text stays unchanged and only subsequent input saves again.
  Tests: `web/test/drafts.test.ts` and
  `web/test/drafts-saving.test.ts` (ordering and trimming).
- **Stopping is not removal:** a `ribbitto.signed-out.v1` notice calls `stop()`:
  it stops writes, cancels saves and hides markers, deleting nothing. The
  notice is best effort; stale writes stay bound to the ended session, are
  never restored and are removed by later cleanup.
- **Removal:** `sweepKeys` runs on sign-in with a signed-out horizon and on the
  next organisation page load, never on BFCache restore. It removes malformed
  `ribbitto.draft.v1:` keys, invalid own entries, own-session keys under another member, and foreign
  sessions strictly older in milliseconds than the horizon; same-millisecond
  and later foreign sessions remain unread. Successful sign-out also sends
  `Clear-Site-Data: "storage"` where supported; failure does neither redirect
  nor clear storage. Either expiry clock stops writes until reload and removes
  that session's keys without reading values. Page text and sending stay usable;
  storage errors can leave copies for later cleanup. See [decision 37 (d)][draft-policy]
  for guarantees and qualifications.

[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
[draft-policy]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md#d-drafts-on-this-device-1178-1181-1162-1177
