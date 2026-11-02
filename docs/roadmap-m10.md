# M10: Front-end architecture

Goal and plan for milestone M10 (#2060) in the [roadmap](roadmap.md#after-the-mvp):
a front end whose complexity stays bounded as reactions, editing, uploads and
search arrive. Each state has one owner, page lifecycles are explicit, and the
stream stays safe across reconnects.

Principle: Go provides truth and HTML. TypeScript owns client state and
decides when server HTML is applied. No swap or reconnect loses, duplicates or
resets what the member is doing.

## Outcomes

1. **Page roots.** Each page kind starts its modules from one page root with
   an explicit lifecycle (start, freeze, thaw, dispose), independent of
   script order.
2. **One cursor owner.** One DOM-free module decides every conversation
   cursor. Frames are classified by kind, and each frame's outcome is recorded.
3. **Swap zones.** Every swap zone has one owner. Tests prove that no swap
   loses typed text, pending posts or their keys.
4. **Recorded stream gaps.** Today's failure-and-reconnect gaps are recorded
   by tests. If A-F (a client-side note on
   [decision 24](decisions/24-a-cursor-that-cannot-be-served-gets-reset.md))
   is approved, no message is skipped after an application failure.
5. **Documented and checked.** State classes, owners and swap zones are
   documented on a new architecture page (started by #2062), and checks keep
   import direction and module purity.

## Position in the roadmap

M10 starts after [M9](roadmap-m9.md): it builds on M9's conversions (#1825,
#1417, #1827, #1826, #1420, #1829, #1830, #1831) and waits for #1426's
milestone measurement. It comes before the features it prepares for (#1361's
A2 and A3: reactions, editing, deleting, uploads). The tests-only A0a (#2062)
and A0b (#2063) change no served file and may run earlier. Placing them is
the maintainer's choice.

## Order

1. **Safety net (tests only):** #2062 swap zones · #2063 reconnect and
   failure behaviour. Both merge before any migration issue.
2. **Lifecycle and roots:** #2064 page lifecycle → #2066 two page roots,
   after M9's conversions.
3. **Stream:** #2065 Progress model with frame outcomes and a strict mode →
   #2067 Progress as the only cursor writer → #2068 decoder and conversation
   applier.
4. **Composer:** #2069 pending posts settled from the applier's `Delivery`,
   with direct calls to drafts.
5. **Checks and documents:** #2070 import direction, module purity and
   the state page.

Each issue preserves behaviour and states its measurement before `ready`, using
the rows of M9's shared measurement record (#1404).

## Out of scope

- **A-F and Track B** (#2060): fail-closed handling of an application
  failure, rerouting reconnects, an own stream client, TypeScript requests,
  ES modules and lazy chunks. Each needs its own approval, and some need a
  decision change.
- **New client state or features:** #1361 and the features' own issues.
- **A directory layout for `web/src`:** a separate issue, after the checks
  follow it.
