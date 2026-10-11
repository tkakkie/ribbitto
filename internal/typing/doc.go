// Package typing owns per-process channel/topic typing summaries, with no tables
// or event kinds. Accepted channel streams keep activity eligible; owner timers
// expire it five seconds after the last signal, and last close clears it at once.
// Transitions publish distinct-member state before raising typing's generation.
// Reads exclude the viewer from the full set and inspect four cached candidates.
// Signed per-composer generations store nothing at issue and bind all scope IDs.
// Successful posts close their composer and stop the posted scope under the signal
// lock. Owner timers retain closures for one minute; locked acceptance refuses
// signals handled for over ten seconds. After cleanup old composers work again,
// preserving long-open pages but allowing a delayed request five seconds of typing.
// Failed-submission renders reuse their generation; other composers stay usable.
// The random in-memory signing key changes on restart, refusing pre-restart tokens.
// Web authorizes signals and renders localized, viewer-filtered status content
// on latest feeds/topics through the organisation stream; no durable cursor moves.
package typing
