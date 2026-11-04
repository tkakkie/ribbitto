# Documentation

Index of the [ribbitto](../README.md) documentation and how it is written
([decision 46](decisions/46-the-role-of-documentation.md)).

## What belongs in documentation

Documentation holds what the code cannot say about itself: **why** (rationale,
trade-offs, rejected alternatives), **direction** (vision, vocabulary,
boundaries) and **what must never change** (invariants, cross-module rules,
operator procedures). Never document how the code works; update `docs/` only
when a requirement, rationale, operator action or promise changes.

Keep, briefly, even when the code shows them: negative constraints (no package
but `org` decides authorisation), cross-module rules (org-first lock order;
sequence order is commit order), operator procedures (restore, collation
upgrade, reverse-proxy contract) and the AI workflow gates. Unbuilt
implementation designs belong in their issue, not in living documents (keep
product direction, reserved vocabulary and explicit non-goals).
The per-area cleanup issues move existing designs to their issues or delete them; this change does not.
When a document nears its limit, delete narration rather than split; interim
exceptions follow `size-exceptions.txt` (do not move prose into `doc.go`).
Deleting prose needs no justification; adding it must pass the
sentence test below. Keep a new decision within 80 lines (review, not a check).

### The sentence test

Apply in order while writing; the first match decides:

1. Could a check or test enforce it? Write that instead; keep at most one line
   of why and the check's name.
2. Does it describe what the code does, so it would turn false after a
   behaviour-preserving refactor? Put it in a code comment at that place, or
   nowhere.
3. After reading the code it is about to change, would a competent AI still get
   this wrong, or need to read many other packages to learn it? Then it belongs
   in `docs/`, in the right kind of document.
4. Is it about how we got here (issue numbers, dates, who approved)? Put it in
   the issue or pull request.

## Documentation

- [Vision](vision.md) — aims and acceptance criteria
- [Architecture](architecture/README.md) — packages, imports and data flow
- [Domain](domain/README.md) — glossary and index; [invariants](domain/invariants.md), [unread rules](domain/unread.md)
- [UI](ui.md) — design, tokens; [colours and contrast](ui-colours.md), [UI code](ui-code.md), [components](ui-components.md), [markup and accessibility](accessibility.md)
- [Decisions](../DECISIONS.md) — what was decided and why
- [Worktree environments](worktree-env.md) — dev databases, port allocation, health checks and cleanup
- [Roadmap](roadmap.md) · [AI development workflow](workflow/README.md) · [Database development](database.md) · [Database tests](database-tests.md)
- [Message paging plan measurements](message-paging-plans.md)
- [Local development](development.md) — running locally, Make targets and [checks](development.md#checks)
- [Dependency and runtime update coverage](dependency-updates.md) — version-pin inventory, monthly review and triage rules
- [First-run setup and backup restore](setup-and-backup.md) — [first-run setup](setup-and-backup.md#first-run-setup) (`RIBBITTO_SETUP_TOKEN` and `/setup`), sign-up and [restoring a backup](setup-and-backup.md#restoring-a-backup)
