# 1. Go, not Rust

**Decided:** the server is written in Go.
**Why:** most code is written by AI tools and must stay easy for a person to
read and review; Go code reads the same whoever writes it, compiles fast and
AI tools write it reliably. Performance is not a differentiator at this
scale — the database and the network dominate.
**Considered:** Rust — stronger compile-time guarantees and lower memory
use, but AI-written Rust was judged too hard to review.
