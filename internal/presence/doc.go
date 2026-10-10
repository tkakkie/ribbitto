// Package presence owns per-process online state, with no tables or event kinds.
// Every accepted organisation stream counts; the last close has a 30-second
// grace period. Web owns the lifecycle seam so realtime never imports presence.
// State is published before the hub generation; ordered changes and reset are
// current. Rendering remains planned (#768); construction needs no store or wiring.
package presence
