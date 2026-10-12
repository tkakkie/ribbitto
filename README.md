# #832 stage-1 measurements, 2026-10-13

Raw logs of #832's stage 1 (plan: `docs/architecture/stream-sidebar-stage1.md`;
criterion: `docs/architecture/stream-sidebar-stability.md`), run by Claude at
main c706260 (#869). This branch is an archive and is never merged into `main`.
Check it with `shasum -a 256 -c SHA256SUMS`.

- **Host:** darwin/arm64, 10 CPUs, Go 1.27.2, PostgreSQL 18.6 in Docker,
  `max_connections` 100, `superuser_reserved_connections` 3, loopback port
  55435, one application process. The run took 2026-10-12T16:51Z–21:21Z.
- **`host.log`:** before and after each group, the load average, the CPU count,
  the top processes (PID, %CPU, elapsed time and executable name only) and
  container CPU.
- **Test logs:** unmodified `go test -v` output.

| File | Group | Windows |
|---|---|---|
| `group-5-1000.log` | 5 posts/s, 1,000 streams | shared ×3, partitioned ×3 |
| `group-5-5000.log` | 5 posts/s, 5,000 streams | shared ×3, partitioned ×3 |
| `group-20-1000.log` | 20 posts/s, 1,000 streams | shared ×3, partitioned ×3 |
| `group-20-5000.log` | 20 posts/s, 5,000 streams | shared ×3, partitioned ×3 |

`groups.summary` has each invocation's exit status. Each log ends with the
`stage1 group=... recommendation-eligible=...` summaries.

## Reproduce

From the repository root at c706260, with a disposable PostgreSQL server in
`RIBBITTO_TEST_DATABASE_URL` (credentials are not recorded here):

```sh
RIBBITTO_REQUIRE_DB=1 RIBBITTO_STREAM_COST_STAGE1=1 RIBBITTO_STREAM_COST_LONG_GROUP=5/1000 \
  go test -count=1 -v -run '^TestStreamCostStage1$' -timeout 3h ./internal/realtime/
```

Repeat the command for groups 5/5000, 20/1000 and 20/5000, one per invocation, on a quiet host.
