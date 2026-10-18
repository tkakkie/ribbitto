# Rate limits and reverse proxies

## Authentication network limits

In `internal/web/middleware/ratelimit.go`, `POST /signin`,
`/signup` and `/setup` are limited with token buckets, per client and, for
IPv6 clients, also per /48:

- sign-in: a burst of 5, then one every 12 s per client; a /48 gets a burst
  of 10, then one every 6 s, shared by all its /64s;
- sign-up and setup: a burst of 3, then one every 10 minutes per client; a
  /48 gets a burst of 6, then one every 5 minutes.

A request is admitted only when both of its buckets have a token. It then
takes one from each; a refused request takes none and creates no bucket. The
order inside each handler is: is the route open (a closed route answers 404
whatever the bucket or body), then the rate limit (429 with a localised
page), then parsing and hashing.

Each limiter keeps at most 10,000 client buckets and 10,000 /48 buckets. A
bucket is dropped only once it has refilled completely, counted from the
last token taken, so a stream of refused requests cannot keep it alive. A
full table refuses new clients (fail closed) rather than sharing a bucket,
which would hand out a second burst.

The /48 budget bounds how many client buckets one /48 keeps unevictable.

- A client bucket stays unevictable for burst × interval after its last
  admitted request: 60 s for sign-in, 30 minutes for sign-up and setup.
- In that window a /48 is admitted at most 10 + 60/6 = 20 sign-in requests,
  or 6 + 30/5 = 12 sign-up or setup requests, so it holds at most that many
  unevictable client buckets. It may leave more evictable ones behind, but
  those go as soon as the table is full.
- Pinning a 10,000-entry client table (keeping it full of unevictable
  buckets) therefore takes at least 500 distinct /48s for sign-in, or 834
  for sign-up and setup. Pinning the /48 table takes 10,000 /48s.
- An IPv4-only attack needs 10,000 addresses to pin a client table. IPv4
  addresses and IPv6 /64s share that table, so a mix of both can pin it
  together.

Whoever holds that many addresses can still turn new clients away for a
while; that is accepted. The /48 is this application's choice, not a standard: unrelated
clients whose smaller prefixes share a /48 share its budget. Limits live in
memory, per process. These authentication network limits are unchanged by
the separate member limiters below; an authentication identity-side layer
is tracked in #99.

The client is the peer's IPv4 address or IPv6 /64, or, behind a trusted
proxy, the address it forwards (below).

## Member admission for typing

`internal/web/middleware/member_ratelimit.go` supplies `MemberRateLimiter`
for typing signals: burst 4, then one token per second. Its only key is the
trusted organisation/member ID pair supplied by the authenticated web
adapter, never client-submitted identity. One shared instance gives that
member one budget across channels, topics, sessions and client addresses;
different members and organisations have separate budgets. It performs no
authorization, feature-state access or database work. The topic-scoped
[typing POST](typing.md#http-ingress-and-posting) checks CSRF and resolves session
and organisation membership first, then admits against this budget before
channel/topic lookups or form parsing. A refusal is empty 429 and changes no
typing state, deadline or generation; an admitted signal is empty 204.

The table holds at most 10,000 buckets. When a new key needs room, only
fully replenished buckets can be evicted: replacing one with a fresh bucket
cannot grant extra tokens. A full table with no such bucket refuses new
keys. Refused requests create no buckets, take no tokens and do not extend
retention. Admission and eviction are serialized, including concurrent
requests for the same member.

This limiter also lives in memory per process: share one instance across
all typing signal callers. Restarting resets the budgets; multiple server
processes have independent budgets. There are no new configuration settings
or distributed limits.

## Member admission for reads

Read POSTs use a separate `MemberRateLimiter`: initial, unmeasured 4 requests/s,
burst 16 and four concurrent requests per trusted organisation/member pair.
Typing keeps its existing independent budget. Admission follows session and
membership resolution (non-members still get 404), before channel/topic lookups,
form parsing and the read transaction. Refusal is immediate 429 with
`Retry-After: 1`; a defer releases each admitted slot on success, error, panic
or cancellation. The 10,000-bucket bound and full-refill eviction rule also
apply here, but a bucket with an active slot is never evicted. Refusals take
no tokens or slots and do not extend retention. State is in memory, per
process; restart resets it and multiple processes have independent budgets.
PR #860's client retries 429s until success or no longer needed. Some of 16
tabs revealing together can be refused. With healthy service and subsiding
competing load, all pending pages converge without another user action;
there is no success guarantee under sustained overload.

## Member admission for channel creation

Channel creation requires an independent `Services.CreateLimits` instance
(shared with neither reads, typing nor branching); missing or shared wiring fails startup.
Its `MemberRateLimiter` allows burst 10, then
one token per minute per trusted organisation/member pair. Session and
membership resolution precede admission, which precedes form parsing and
all channel work. Refusal is empty 429 with `Retry-After: 60` and creates
nothing. Failed admitted attempts also consume a token. Other members and
organisations have independent budgets, as do typing and reads. The same
10,000-bucket bound and full-refill eviction rule apply. State is in memory
per process; restarting resets it and multiple processes have independent
budgets. The separate persistent count cap is described in
[channels](../domain/channels.md#creation-limits).

## Member admission for sidebar resyncs

The sidebar resync POST requires an independent `Services.ResyncLimits`
instance, shared with neither typing, reads, creation nor branching; missing or shared
wiring fails startup. Its `MemberRateLimiter` allows an initial, unmeasured
32 requests/s with burst 32 per trusted organisation/member pair, across
all tabs, sessions and addresses. The script's exponential backoff starts
at 250 ms and is capped at 8 s, each wait between half and all of it: at the
cap, 16 tabs retry at most 4 times/s.
The budget leaves steady retry headroom and a burst of two requests per tab
for visibility return, persisted `pageshow` and early recovery retries.

CSRF, session and membership checks precede admission; non-members still
get 404. Admission precedes `RequestResync`. Refusal is empty 429 with
`Retry-After: 1` and never advances the member's watch generation or wakes
streams. Success is empty 204 and publishes only to that member's streams.
Other members and organisations have independent budgets. The same
10,000-bucket bound and full-refill eviction rule apply. State is in memory
per process; restarting resets it and multiple processes have independent
budgets. A 429 follows the script's failed-request path: recovery stays
open and retries with backoff. Only a successful request with an applied,
covering snapshot and no intervening loss clears recovery; overload has no
convergence guarantee.

## Member admission for branching

Branch POSTs require independent `Services.BranchLimits`; missing or shared
wiring fails startup. Burst 20, then one token every 6 s per trusted
organisation/member: enough to tidy a channel, while moves recount each
viewer's sidebar at most ~10 times/min. CSRF, session and
membership precede admission; parsing and branch work follow it. Failed
attempts spend tokens. Refusal is empty 429 with `Retry-After: 6`, without
redirect, move or event, for plain and htmx forms. Channels, topics, sessions
and addresses share it. Other members and organisations have independent
budgets. The same 10,000-bucket bound, full-refill eviction
and per-process reset rules apply.

## Reverse proxies

ribbitto works behind any reverse proxy (Caddy, nginx, HAProxy, Traefik…)
meeting this contract; security does not rely on any one proxy.

- `RIBBITTO_TRUSTED_PROXIES` lists the **proxy peers trusted to forward the
  client address to ribbitto**, not trusted networks. It is comma-separated
  CIDRs, with `/32` or `/128` for one address; bare addresses and invalid
  CIDRs fail startup.
- Forwarded client addresses are not trusted by default. If the request's
  immediate peer is not in the list, `X-Forwarded-For` is ignored and the
  peer address is the key. Only when the peer is a trusted proxy is the
  header read.
- The header is read from the right, skipping trusted hops, and the first
  untrusted address is the client. A client therefore cannot choose its key
  by adding entries on the left. The request falls back to the peer when the
  header is missing, lists only trusted proxies, or has a malformed entry at
  or right of that address. Entries further left are the client's own and
  are never parsed.
- **What a trusted proxy must do:** append the address of the peer it
  received the request from to `X-Forwarded-For`, or replace the header with
  a client address it resolved through its own explicitly trusted chain. It
  must never pass a client-supplied address on as authoritative. A proxy
  that forwards the header unchanged lets any client choose its key, however
  narrowly it is listed.
- **What to list:** the proxies themselves, never ordinary clients. A stable
  proxy is listed as its own `/32` or `/128`. A broader CIDR is fine only
  when every peer that can reach ribbitto from it may be trusted as a
  forwarding proxy. Otherwise a client inside it can write its own key.
- **Several proxies:** preferably, the proxy directly in front of ribbitto
  resolves and forwards the client address, and ribbitto trusts only it. If
  it merely appends the previous proxy's address, every client behind that
  proxy shares one key; trusting several controlled hops explicitly is also
  allowed.
- **Empty (the default):** `X-Forwarded-For` is never trusted. Behind a
  proxy, all clients then share the proxy's key and may be rate-limited
  together; this default is safer than trusting an untrusted header.
  Authentication requests with this header from loopback or private peers
  warn once per `AuthLimits`, without logging addresses.
- Addresses and CIDRs are compared in their IPv4 form when they are
  IPv4-mapped; an IPv4-mapped CIDR in `RIBBITTO_TRUSTED_PROXIES` needs a
  prefix length of at least `/96`, or startup fails.

The official self-hosting setup will use Caddy under this contract. Caddy
reads the client address from the left unless `trusted_proxies_strict` is
set, so the shipped configuration must set it or overwrite the header.
Examples for other proxies can be added against the same contract.
