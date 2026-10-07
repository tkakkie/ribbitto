# Load test results

End-to-end results of #216: how many concurrent event streams ribbitto
serves, and what breaks first. The assumptions and dispositions are in
[load testing](load-testing.md), the client in [load client](load-client.md),
and the in-process cost per post in [stream cost](stream-cost.md).

## Run of 2026-10-07

**Machine:** one disposable Linux container (maintainer's decision on #216). It
ran in Docker Desktop's Linux VM (kernel 7.0.14-linuxkit, 10 CPUs, 7.7 GiB),
which shares an Apple M5 host with macOS.

- The container had no network (`--network none`): the server, PostgreSQL
  18.6 (default settings, `max_connections` 100), Caddy 2.11.7 and the load
  client all talked over loopback.
- **Limits:** file descriptors 1,048,576 (`--ulimit nofile`), ephemeral ports
  1024–65535, `somaxconn` 65535, `tcp_tw_reuse` 1.
- **Server:** a linux/arm64 build of `main` at 50ca8b0 (Go 1.27.1), using
  pgx's default pool of 10 connections.

**Workload:**
- **Fixtures:** one organisation with 3,750 accounts, each with one session
  and at most 16 streams (`cmd/seed -streams 60000`).
- **Streams:** every stream follows the first fixture channel's latest feed,
  from the page's cursor. The client sends no language, so every stream gets
  the default language.
- **Posts:** posts go to that channel's default topic. A post's body is its
  marker, which renders to about 1.2 KB of HTML.
- **Steps:**
  - *active:* 10 posts/s for 30 s;
  - *idle:* no posts for 60 s.
  - Each step allows up to 5 minutes to set the streams up and 60 s to drain.
  - Each series restarts the server and stops at its first step that does not pass.
- **Transports:**
  - *HTTP/1.1:* straight to the server, from source addresses `127.0.0.2` to
    `127.0.0.9`;
  - *HTTP/2:* through Caddy (`tls internal`, `reverse_proxy` with
    `flush_interval -1`), which reaches the server over HTTP/1.1 with one
    connection per stream.

Every 2 s a sampler also read the server's, Caddy's and the client's open
file descriptors (`/proc/<pid>/fd`), their RSS and CPU, and the VM's available
memory.

### Active steps: 10 posts/s

| Streams | HTTP/1.1 p50 / p95 | HTTP/2 p50 / p95 | Queries per post | Empty-acquire wait | Result |
|---|---|---|---|---|---|
| 500 | 14 / 24 ms | 16 / 24 ms | 522 | 6–7 ms | pass |
| 1,000 | 16 / 24 ms | 16 / 24 ms | 1,029 | 8 ms | pass |
| 2,000 | 22 / 34 ms | 22 / 34 ms | 2,042 | 13–14 ms | pass |
| 3,000 | 28 / 43 ms | 29 / 45 ms | 3,056 | 18–19 ms | pass |
| 5,000 | 41 / 66 ms | 43 / 69 ms | 5,083 | 29–31 ms | pass |
| 7,000 | 60 / 103 ms | 85 / 330 ms | 7,111 | 41–46 ms | pass |
| 10,000 | 1.7 / 2.8 s | 2.4 / 4.3 s | 10,148 | 83–89 ms | fail: p95 over 1 s |

- **Latency** is post-to-receipt: from issuing the POST to the event arriving
  on a stream.
- **Empty-acquire wait** is the mean wait of a pool acquisition that found
  no free connection.
- **Every step completed:**
  - all 300 scheduled posts were sent and answered;
  - there were no failed or reset streams;
  - no delivery was missing; at 10,000 streams the drain took 2.8 s
    (HTTP/1.1) and 4.6 s (HTTP/2).

**What hit the limit first: the server's database pool.**
- **Queries:** a post costs about one query per open stream, plus about 20.
  That one query is the membership check each connection makes before
  sending each event ([streaming](streaming.md#authorization-and-revocation)).
  At 10,000 streams and 10 posts/s that is about 100,000 queries a second.
- **Pool:** the mean wait for a connection grows with the stream count.
- **CPU had room:** CPU was sampled every 2 s in reruns of the 7,000 and
  10,000 steps. At 10,000 streams over HTTP/1.1, the VM was 68% busy: the
  server used about 3.5 CPUs, PostgreSQL 2 and the client 1.5. Over HTTP/2 the
  VM was 80% busy, with Caddy using about 2.5 CPUs.
- **A larger pool moves the ceiling, but does not remove it.** With
  `pool_max_conns=40`, 10,000 streams pass (p95 130 ms), and 15,000 fail
  (p95 3.8 s), with the VM 88% busy and PostgreSQL near 4 CPUs.
- **Not the rest:** the shared work per post (event read, render, the
  zero-limit read noted on #216) stays at about 20 queries whatever the
  stream count, so it is not worth optimising first.
- **Over HTTP/2:** p95 is higher from 7,000 streams because Caddy shares the
  machine.
- **Run-to-run variation:** the reruns' p95 at 7,000 streams was 136 ms over
  HTTP/1.1 and 337 ms over HTTP/2, against 103 and 330 ms in the table.
  The pass and fail steps were the same.

### Idle steps: no posts

| Streams | HTTP/1.1: server RSS | HTTP/1.1: least free memory | HTTP/2: server RSS | HTTP/2: Caddy RSS | HTTP/2: least free memory | Result |
|---|---|---|---|---|---|---|
| 10,000 | 446 MB | 6.0 GB | 457 MB | 1.9 GB* | 4.1 GB | pass |
| 20,000 | 895 MB | 5.1 GB | 897 MB | 2.3 GB | 2.7 GB | pass |
| 30,000 | 1.5 GB | 4.0 GB | 1.5 GB | 3.3 GB | 0.5 GB | pass |
| 40,000 | 1.9 GB | 3.1 GB | 1.9 GB | 4.0 GB | 0.15 GB | pass |
| 50,000 | 3.0 GB | 1.6 GB | — | 4.1 GB | 0.07 GB | HTTP/2 fail |
| 60,000 | 3.3 GB | 0.8 GB | — | — | — | HTTP/1.1 pass |

\* Caddy was not restarted between the HTTP/2 series, so its RSS includes
what the active steps left behind.

- **Server, per stream:** about 50–57 KB of RSS, 28 KB of heap in use, two
  goroutines and one file descriptor. At 60,000 streams it held 60,019
  descriptors.
- **Set-up:** about four queries per stream, done in 0.4 s for 10,000 streams
  and 2.8 s for 60,000.
- **HTTP/1.1 ceiling: none found up to 60,000**, the fixtures' size, so 60,000
  is a lower bound. Memory would be next: 0.8 GB was left.
- **HTTP/2 ceiling: 40,000 passes and 50,000 fails, on the proxy's memory.**
  - Caddy grew by about 70–100 KB per stream, roughly twice the server.
  - At 50,000 attempts, 38,500 streams were established when the VM ran out of
    memory (67 MB left); the kernel's OOM killer stopped Caddy (the
    container's cgroup counted one `oom_kill`).
  - Every stream then failed, and the client's further dials were refused.
  - The server and PostgreSQL stayed up.
- **The client** used about 38 KB per stream over HTTP/1.1 and 25 KB over
  HTTP/2. Over HTTP/2 it opened 874–5,421 TCP connections, because Go's client
  dials another when a connection is busy.

## Reproducing it

Inside a disposable container on a disposable machine, never against data you
keep:
- **Image:** `postgres:18` plus Caddy's binary (`COPY --from=caddy:2`), with
  `procps`, `iproute2`, `curl` and `jq`.
- **Container:** `docker run` with `--network none`, `--ulimit
  nofile=1048576:1048576`, `--sysctl net.ipv4.ip_local_port_range="1024
  65535"`, `--sysctl net.core.somaxconn=65535`, `--sysctl
  net.ipv4.tcp_tw_reuse=1`, `-e POSTGRES_HOST_AUTH_METHOD=trust` (passwordless,
  but only reachable on the container's loopback) and `-c
  listen_addresses=127.0.0.1`.

Inside it, with `RIBBITTO_DATABASE_URL` pointing at a fresh `ribbitto`
database on `127.0.0.1`:

```sh
ribbitto migrate up
seed -messages 100 -streams 60000 -streams-per-account 16 \
  -sessions-per-account 1 -output /root/loadtest.json
caddy run --config Caddyfile   # https://localhost:8443 → 127.0.0.1:8080
RIBBITTO_ADDR=127.0.0.1:8080 RIBBITTO_DEV_METRICS_ADDR=127.0.0.1:9090 ribbitto serve
```

Then run each series as [load client](load-client.md#a-ceiling-search)
shows, with `-metrics http://127.0.0.1:9090 -setup 5m -drain 60s
-dial-concurrency 256`:
- *active:* `-rate 10 -duration 30s` at 500, 1,000, 2,000, 3,000, 5,000,
  7,000, 10,000 … streams;
- *idle:* `-rate 0 -duration 60s` at 10,000 … 60,000 streams;
- *HTTP/1.1:* add `-source 127.0.0.2,…,127.0.0.9`;
- *HTTP/2:* use `-target https://localhost:8443 -ca <Caddy's root.crt>`.
