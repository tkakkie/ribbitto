# Rate limits and reverse proxies

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
memory, per process. Throttling is network-only today; the future design of
an identity-side layer is tracked in #99.

The client is the peer's IPv4 address or IPv6 /64, or, behind a trusted
proxy, the address it forwards (below).

## Reverse proxies

ribbitto works behind any reverse proxy (Caddy, nginx, HAProxy, Traefik…)
that meets this contract. Its security does not depend on any one proxy's
behaviour.

- `RIBBITTO_TRUSTED_PROXIES` lists the **proxy peers trusted to forward the
  client address to ribbitto**, not trusted networks. It is comma-separated
  CIDRs, with `/32` or `/128` for a single address; a bare address fails at
  startup, and so does an invalid CIDR.
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
- **Several proxies:** the recommended setup is for the proxy directly in
  front of ribbitto to resolve the client address and forward it, and for
  ribbitto to trust only that proxy. If it merely appends the previous
  proxy's address, every client behind that proxy shares one key. Trusting
  several controlled hops explicitly is also allowed.
- **Empty (the default):** `X-Forwarded-For` is never trusted. Behind a
  proxy, all clients then share the proxy's key and may be rate-limited
  together. That is kept as the default, because it is safer than believing
  an untrusted header.
- Addresses and CIDRs are compared in their IPv4 form when they are
  IPv4-mapped; an IPv4-mapped CIDR in `RIBBITTO_TRUSTED_PROXIES` needs a
  prefix length of at least `/96`, or startup fails.

The official self-hosting setup will use Caddy as one configuration that
meets this contract. Caddy reads the client address from the left unless
`trusted_proxies_strict` is set, so the shipped configuration must set it or
overwrite the header. Examples for other proxies can be added against the
same contract.
