# Hypothetical hybrid sidebar verification (#960b)

The [contract](sidebar-hybrid-contract.md) domain runner is reused by four
independent conditions; production-only retains its original invalidations and
read writer. No condition borrows another's metadata or the oracle's results.

- **Author:** committed non-author posts add one to the channel, listed topic
  and selection. Own posts reconcile: author identity cannot settle page reads.
- **Read ranges:** test-only SQL fixture unions and a coverage revision commit in
  one transaction. Publications reload persisted revisions after successful commit.
  Independently captured message identities, sequences and read status let exact
  coverage decrement only previously unread messages. Repeated/overlapping reads
  and replay do not double decrement. Missing coverage/revision or an unconfirmed
  outcome reconciles. Posts, moves and list changes still reconcile.
- **Absolute count:** the fixture read transaction runs an extra exact channel
  count and persists it for publication. Only another channel, without displayed
  topic/selection entries, permits a full-model delta. Current-channel reads
  reconcile because a total cannot determine topic or selection counts. This is
  no performance claim; the additional count must be charged in #952.

Every operation records its condition, delta/reconciliation and reason. Writes
remain stopped at the shared persisted boundary through all tab verdicts.
Equality includes uncapped/capped counts, badges, lists, selection and other
channels, immediately for deltas and after gated completion for reconciliation.
Fixture receipts are not production publisher or transaction verification.
Ordering, generations, no rollback and continuous-posting progress remain #961.
