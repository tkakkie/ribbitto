# Write-time unread counters: fixed workload (#957c4a)

This is a pre-measurement protocol, **unmeasured**. The test-only schedule and
dispatch are in `internal/realtime/unread_counters_workload_test.go`; accounting
uses the [existing SQL seam](unread-counters-sql-adapter.md). Constants are fixed
before evidence, with no design or database input to the relative slot plan.

| Input | Fixed value and reason |
|---|---|
| Streams / posts | 5,000 distinct members; 5 and 20 posts/s separately, as required by #957. Cursorless posts read only the author's new message. |
| Read advances | 100/s, feed/topic 50:50, consistent with #952's 100 persisted changed reads/s. No deliberate repeat slots; #957c4b counts persisted changes and no-ops separately. |
| Read targets | Alternate feed/topic slots, rotate all 5,000 members every 50 s, then advance the cursor by two sequences. |
| Move cadence | One every 10 s, rotating 100, 1,000, 5,000 member-row cohorts. This isolates two orders of magnitude of every-member locking while holding the message batch fixed. |
| Move batch | Four original messages, alternating direction each cohort visit. Reuse prevents running out of move inputs; notices remain real production writes. |
| Move states | Equal quarters: all four unread, all four read, alternating two read with a valid bundle, the same two read with a missing bundle. Missing bundles expose lazy fallback without suppressing member locks. |
| Destination floor | Initially above all four originals, even for unread members: floors must never make moved-in unread messages read. |
| Joins | One real sign-up every 5 s, with a distinct account/handle and committed join prefix; no eager bundles. Low steady onboarding adds ordered writes without growing the fixed move cohorts. |
| Detection / repair | Every 5 s / 30 s, on the posting member's main channel. Detection scans T and R independently; unconditional scheduled repair charges rebuild work even when detection finds no drift. These are separate operations, never write maintenance. |

Slots include zero and exclude the window end; ties are post, read, move, join,
detection, repair. In 30 virtual seconds each design receives 150 or 600 posts,
3,000 advancing reads (1,500 feed and 1,500 topic), three moves, six joins, six
detections and one repair. Successful persisted changes and no-ops must be reported
separately by #957c4b, never inferred from slot labels. That part must preserve
per-member ordering and count expired slots.

Both designs seed the same interleaved main-channel history sized for the whole
window at 100 advances/s (two messages per member rotation), independent of live
post rate; both also seed the same read ranges, floors, real counter rows and
missing quarter. Move cohort sizes count active channel member rows, not all
organisation members who can access a public channel.
Joins do not enlarge these cohorts. Both designs share the existing 35 application
plus one observer connection budget; current-design detection/repair slots do no
SQL because that design has no counters to rebuild. Fixture and oracle SQL use
the untraced pool. Whole-coverage maintenance and the bytea carrier are excluded.

#957c4b supplies stream integration, completed/missed/failed and persisted-read
results, drain/final-model checks, and existing telemetry under unchanged #867.
Deterministic tests here inspect exact slots and execute representative dispatches
in both designs, including all three move sizes in both directions. They provide
correctness evidence only. No smoke, measurement or adoption decision is included.
