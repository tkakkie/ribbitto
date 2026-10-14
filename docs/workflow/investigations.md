# Investigations and the performance target

- New measurement or verification stages, prototypes and measurement
  infrastructure need explicit maintainer approval before work starts.
- Investigation and optimisation issues state a pull-request or time cap
  before work starts. Stop and report at the cap, even without a conclusion.
- The target is 5,000 concurrent connections per process, with message arrival
  not much worse than without unread counts. Check 2 vCPU / 4 GB in a separate
  issue once available; development-machine runs decide until then.
