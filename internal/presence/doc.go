// Package presence owns per-process online state, with no tables or event kinds.
// Every accepted organisation stream counts; the last close has a 30-second
// grace period. Web owns the lifecycle seam so realtime never imports presence.
// State is published before the hub generation; delivery remains planned (#767,
// #768), and construction needs neither a store nor a wiring package.
package presence
