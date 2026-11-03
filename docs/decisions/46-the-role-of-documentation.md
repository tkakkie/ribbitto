# 46. The role of documentation

**Decided** (#2102, approved by the maintainer on 2026-11-03; pre-migration
commit `e91787814314a05a00c37b293a375bb7d1c9ae3e`, the `origin/main` tip at
the final merge of main into this PR).

Documentation holds what the code cannot say about itself: **why** (rationale,
trade-offs, rejected alternatives), **direction** (vision, vocabulary,
boundaries) and **what must never change** (invariants, cross-module rules,
operator procedures). It does not mirror or narrate the current implementation.
"Derivable from code" is not enough on its own to delete a claim: code shows
what happens today, including bugs, not what must happen. Keep, briefly, even
when code shows them: negative constraints (no package but `org` decides
authorisation), cross-module rules (org-first lock order; sequence order is
commit order) and operator procedures (restore, collation upgrade,
reverse-proxy contract).

## The sentence test

Apply in order; the first match decides:

1. Could a check or test enforce it? Write that instead; keep at most one line
   of why and the check's name.
2. Does it describe what the code does, so it would turn false after a
   behaviour-preserving refactor? Put it in a code comment at that place, or
   nowhere.
3. After reading the code it is about to change, would a competent AI still get
   this wrong, or need to read many other packages to learn it? Then it belongs
   in `docs/`, in the right kind of document.
4. Is it about how we got here (issue numbers, dates, who approved)? Issue or PR.

## Document kinds

- **Vision, roadmap and domain:** product aims, milestone goals and non-goals,
  product and code vocabulary, domain terms, cross-cutting invariants and
  user-visible domain rules.
- **Architecture, UI and operations:** package boundaries, allowed imports,
  cross-feature access rules, real-time or security contracts across packages,
  visual direction, tokens, accessibility and operator procedures.
- **Unbuilt designs (`planned`):** unbuilt implementation designs belong in their issue,
  not in living documents, and a plan that no longer matches reality is deleted. Keep
  product direction, reserved vocabulary and explicit non-goals.
  The per-area cleanup issues move existing designs to their issues or delete them; this change does not.
- **Measurements:** short frozen records of conclusions, conditions and measured SHA;
  delete raw results and record the actual pre-deletion SHA (answer 1 below).
- **Decisions:** historical records of long-lived choices, why they were made and
  what was considered.

## Maintainer questions and answers

1. Measurements and paging/recount plans? Keep short frozen records; delete raw results.
2. Decisions 26–44? Keep their text, numbers and files unchanged, now and later.
3. Workflow consolidation? Remove duplication only; preserve independence, gates and failure handling.
4. Generated references? Keep them unchanged; whether to stop committing them is a separate issue.

## Rules

- **Decisions are not edited after merge.** Old decisions stay as written, now and later;
  decisions 26 to 44 keep their text, numbers and files unchanged because code cites them
  (answer 2 above). The only permitted later edits are (a) a "Superseded by N" line
  and (b) repairing a link whose target moved or was deleted (retarget it, or replace it
  with plain text naming the path and the pre-migration SHA
  above, including measurement links from decisions 31, 32 and 34).
- **Delete rather than split.** A growing total is cured by deleting implementation
  narration and consolidating, never by splitting a topic across files. Prose does not
  move into `doc.go`, where comments explain why, not what.

**Why:** instructing every pull request to update `docs/` and splitting files on overflow
turned documentation into implementation narration that goes stale and misleads readers.

**Considered:** keeping split-on-overflow or raising per-file limits (narration keeps
growing); deleting everything derivable from code (drops negative constraints,
cross-module rules and operator procedures); shortening decisions 26–44 (obscures history
and breaks code citations); moving deleted prose into `doc.go` (shifts narration into Go).
