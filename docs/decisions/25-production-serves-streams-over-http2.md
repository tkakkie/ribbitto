# 25. Production serves event streams over HTTP/2

**Decided:** a production deployment serves ribbitto over HTTP/2 (Caddy by
default, as in [decision 7](07-rental-vps-and-containers.md)). Each open
channel page holds one event stream.
**Why:** browsers allow only six HTTP/1.1 connections per origin, so a few
open tabs would block every other request; HTTP/2 carries all streams of an
origin on one connection. Details:
[streaming](../architecture/streaming.md#resource-limits).
**Considered:** sharing one stream between tabs in the browser (more client
code, deferred); WebSockets ([decision 3](03-server-sent-events-plus-post-not-websockets.md)
keeps Server-Sent Events).
