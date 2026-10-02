# 4. PostgreSQL with sqlc and goose

**Decided:** PostgreSQL 18; queries written in SQL and turned into typed Go
by sqlc; schema changes as goose SQL migrations embedded in the binary and
applied only by `ribbitto migrate`.
**Why:** row-level security, `uuidv7()`, transactional DDL; SQL stays
visible and reviewable; nothing changes the schema implicitly at start-up.
**Considered:** SQLite (simpler to run, weaker for concurrent writers and
without row-level security); an ORM (hides the SQL that most needs review).
