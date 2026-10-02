# 7. Rental VPS and containers

**Decided:** deploy as containers (app, PostgreSQL, Caddy) with Docker
Compose on a rental VPS; publish images to GHCR. The provider is chosen
before M3.
**Why:** long-lived SSE connections and an in-memory hub need a long-running
process; the same Compose file serves self-hosters.
**Considered:** scale-to-zero platforms with request time limits (break
SSE); managed databases (more cost, not needed yet). Backups outside the
VPS are required before going public.
