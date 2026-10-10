// Package typing owns per-process channel/topic typing summaries, with no tables
// or event kinds. Explicit starts and stops publish distinct-member state before
// raising realtime's typing generation. Reads exclude the viewer from the full
// set and inspect at most four cached candidates. Expiry and accepted-stream
// lifetime, authorized ingress and web rendering/delivery are subsequent parts.
package typing
