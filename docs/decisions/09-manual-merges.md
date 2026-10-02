# 9. Manual merges

**Decided:** the maintainer merges every pull request by hand; there is no
auto-merge yet.
**Why:** process is added only when a problem appears. Automating merges is
worth it only once manual merging is actually a burden; if it comes, it
starts with documentation-only changes and checks that the head commit is
the one CI passed.
**Considered:** path-based auto-merge from the start (more machinery than the
current volume justifies).
