# 10. Argon2id parameters and a cap on concurrent hashing

**Decided:** new password hashes use Argon2id with 19 MiB of memory, 2
iterations and parallelism 1, a 16-byte salt and a 32-byte key, stored as a
PHC string. Verification accepts other parameters only within fixed bounds
(8–64 MiB, 1–10 iterations, parallelism 1–4, 16–64-byte salt and key). Each
`auth.Hasher` has `min(GOMAXPROCS, 4)` slots; a caller waits at most 5 s for
one, otherwise the request fails with 503. The cap holds per process only
because of a wiring rule: in production, `cmd/ribbitto` must create exactly
one `Hasher` per process and share it with every authentication use case,
so that all hashing and verification at run time go through the same
slots. That wiring is added by the first change that uses the hasher in a
real use case (setup or sign-in), not together with the hasher itself.
There is no package-level semaphore: `AGENTS.md` rules out global state.
**Why:** these are OWASP's minimum recommended parameters, cheap enough for a
small VPS. The slots bound concurrent Argon2id work: its working memory is
about 76 MiB with the defaults and 256 MiB in the worst case the bounds
allow. These are estimates for Argon2id alone, not ceilings for the whole
process, and sustained traffic can still keep the CPU busy (rate limits,
#33, address that). The PHC string records the parameters, so
they can be raised later without invalidating stored hashes; the bounds
(and a length check before parsing) keep a corrupt or planted hash from
panicking the process or allocating unbounded memory.
**Considered:** RFC 9106's 64 MiB profile (too much memory per hash for a
small server with several sign-ins at once); bcrypt (truncates passwords at
72 bytes and is not memory-hard); no cap (a burst of sign-ins could exhaust
memory).
