# 25. Production serves event streams over HTTP/2

**Decided:** a production deployment serves ribbitto over HTTP/2 (Caddy by
default, as in [decision 7](07-rental-vps-and-containers.md)).
**Why:** browsers allow only six HTTP/1.1 connections per origin, and in M3
every open latest channel page holds an event stream, so a few open tabs
would block every other request; HTTP/2 carries all streams of an
origin on one connection. Details:
[stream limits](../architecture/stream-limits.md).
**Considered:** sharing one stream between tabs in the browser (more client
code, deferred); WebSockets ([decision 3](03-server-sent-events-plus-post-not-websockets.md)
keeps Server-Sent Events).
