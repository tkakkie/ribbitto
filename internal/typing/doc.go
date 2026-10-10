// Package typing owns per-process channel/topic typing summaries, with no tables
// or event kinds. Accepted channel streams keep activity eligible; owner timers
// expire it five seconds after the last signal, and last close clears it at once.
// Transitions publish distinct-member state before raising typing's generation.
// Reads exclude the viewer from the full set and inspect four cached candidates.
// Authorized ingress and web rendering/delivery are subsequent parts.
package typing
