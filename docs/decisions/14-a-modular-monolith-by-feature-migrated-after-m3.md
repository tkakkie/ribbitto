# 14. A modular monolith by feature, migrated after M3

**Decided:** ribbitto moves towards a modular monolith organised by
feature. The provisional modules are `identity` (accounts, passwords,
sessions), `org` (organisations, memberships, authorisation), `channel`,
`message` and `realtime`; later unread, presence, files and search. The
shared kernel is the IDs and domain value types, the per-organisation
`event_seq`, and the authorisation entry point. The code migrates after M3,
one module at a time, adding Go `internal` directories and per-module
depguard rules then. Until then, the current layering, the placement of
authorisation in `internal/app` and the depguard rules stay as they are;
new code goes into feature packages inside the layers and follows the
feature map in `docs/architecture/features.md`. Recurring shared-file
conflicts, AIs needing unrelated features to do a task, or repeated
boundary findings in review are triggers to reassess this plan in a
separate issue, not permission to migrate early.
**Why:** in M1 every feature touched the same shared files, which cost
several rebase rounds and caused resolution mistakes, and any change needed
most of the architecture document (#2, #106). ribbitto aims at the scope of
Zulip or Mattermost and is built mostly by AI, so this grows with every
feature. The boundaries are not known yet; M2 and M3 will show them. Moving
packages and rewriting imports is mechanical, but separating shared
transactions, APIs and ownership may need design, which is better done
with that evidence.
**Considered:** migrating now (a big rewrite before the boundaries are
known); staying a layered monolith with feature files inside the layers, as
Zulip and Mattermost do at that scope. That their large layer packages make
AI work with limited context hard is a hypothesis, supported by M1 but not
proven.
**Completed by:** [decision 26](26-modules-by-feature-layout-seams-and-order.md) (layout, seams and order).
