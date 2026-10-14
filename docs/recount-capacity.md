# Recount capacity

## Recount capacity configuration

`serve` accepts `RIBBITTO_ASYNC_RECOUNTS=on|off`; unset or empty means off,
and any other value fails startup. Off ignores all seven budget inputs below,
including malformed stray values, and preserves the database URL's pool settings
and pgx defaults. On requires every input explicitly, with no defaults:

| Environment variable | Budget input |
|---|---|
| `RIBBITTO_POOL_MAX_CONNS` | C: pool maximum per process |
| `RIBBITTO_RECOUNT_WORKERS` | W: configured recount workers |
| `RIBBITTO_DB_MAX_CONNECTIONS` | M: PostgreSQL max_connections |
| `RIBBITTO_DB_RESERVED_CONNECTIONS` | R: reserved connections |
| `RIBBITTO_DB_EXTERNAL_CONNECTIONS` | E: external/operations connections |
| `RIBBITTO_DB_PROCESSES` | P: maximum simultaneous ribbitto processes |
| `RIBBITTO_DB_NORMAL_HEADROOM` | H: connections for normal work per process |

All inputs must be signed 32-bit integers: C, W, M and P positive; R, E and H
non-negative. Startup reports all violated input and budget rules together:
`B = floor((M-R-E)/P)`, `0 < C <= B`, `0 < W <= C-H`. Deployment inputs
are operator declarations, not discovered database settings. Include overlap
during deployments in P. The registration limit is the effective
`RIBBITTO_MAX_STREAMS`, with no separate setting.

With M=100, R=3, E=20, P=2 and H=10, B=38. C=36 and W=26 pass;
C=39 or W=27 fail. **36/26 is a validation configuration, not a production
default.** On applies C before opening the pool, overriding `pool_max_conns`
in the database URL. The platform's `PoolOptions.MaxConns=0` preserves existing
pool behavior. A startup `slog` record includes the effective pool maximum,
configured budget, stream registration limit and effective delivery mode;
no database URL or credentials are logged.

On constructs one scheduler with W workers and the process stream cap as its
registration limit, plus one delivery observer. Both share the production
stream composition and development metrics; shutdown joins the scheduler before
closing the shared pool. Off constructs neither and keeps synchronous delivery;
metrics report `scheduler.enabled: false` and `delivery.enabled: false`.
**Async recounts are not enabled by default and are not yet validated for
production.** Stage 2 / PR 3 of #865 decides activation using a freshness oracle
and mixed-load validation; opt-in wiring alone is not that validation.
See the [scheduler contract](architecture/recount-scheduler.md).

Member/account fairness is out of scope: FIFO is per stream, so sixteen tabs
receive more service than one tab. Equal account shares need an identity-aware
policy.

## Current computation sizing (#1046)

Both delivery modes share one computation limit. Async uses W, already checked
against C-H; registrations still use the whole process stream budget. Sync uses
`max(1, floor(C/2))` from the effective pgx pool maximum (including URL settings
and pgx defaults), reserving `ceil(C/2)` connections for normal work when C >= 2.
A one-connection pool permits one recount and cannot reserve a separate normal
connection; deployments needing headroom must provide at least two connections.
No new setting is required: sync derives a conservative default, and async keeps
its explicit existing budget. Startup logs the computation limit. This bounds
sidebar computation only; ordinary page loads retain their exact SQL and gain
no admission limit. The [stream contract](architecture/recount-scheduler.md#current-shared-computation-limit-1046)
spreads connect recounts and defers busy streams without rejecting them.
