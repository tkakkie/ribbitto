# 5. One event sequence per organisation

**Decided:** `organization.event_seq` orders every durable event of an
organisation and is also stored in `message.event_seq`; unread counts
compare against it.
**Why:** one gap-free counter gives replay without loss, correct ordering
and unread tracking that survives `event_log` clean-up.
**Considered:** timestamps (not unique, clock-dependent); a global database
sequence (commit order differs from sequence order, so readers can skip
events). The cost — writes in one organisation serialise on one row — is
acceptable at this scale.
