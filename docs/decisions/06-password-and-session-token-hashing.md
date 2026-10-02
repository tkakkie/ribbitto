# 6. Password and session-token hashing

**Decided:** passwords are hashed with Argon2id; session tokens are 32
random bytes from `crypto/rand`, and only their SHA-256 hash is stored.
**Why:** passwords are low-entropy and need a slow hash; random tokens are
high-entropy, so a fast hash is enough to make a leaked table useless while
keeping every request's lookup cheap.
**Considered:** Argon2id for tokens (needless cost on every request);
storing tokens in plain text (a database leak would hand out sessions).
