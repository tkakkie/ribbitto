package web

const (
	// #1478: DOM traversal/allocation grows with elements; no count headroom.
	feedBudgetElementsFixed = 181
	// #1478: each message's DOM must not grow without justification.
	feedBudgetElementsSlope = 29
	// #1478: fixed forms add request-processing work; zero headroom.
	feedBudgetFormsFixed = 11
	// #1478: message forms add processing work; zero headroom.
	feedBudgetFormsSlope = 1
	// #1478: explicit submits participate in htmx processing; zero headroom.
	feedBudgetSubmitFixed = 7
	// #1500: message submits participate in htmx processing; zero headroom.
	feedBudgetSubmitSlope = 1
	// #1478: fixed hx carriers are an independent markup ratchet; zero headroom.
	feedBudgetHXFixed = 10
	// #1500: message hx carriers must not regrow; zero headroom.
	feedBudgetHXSlope = 0
	// #1478: htmx initializes fixed selected nodes including body; zero headroom.
	feedBudgetHTMXFixed = 21
	// #1500: selected message nodes cost initialization; zero headroom.
	feedBudgetHTMXSlope = 1
	// #1478: bytes allow 5% (rounded up) above the measured fixed part for classes/text/translations.
	feedBudgetBytesFixed = 21769
	// #1500: bytes allow 5% (rounded up) above the measured slope for classes/text/translations.
	feedBudgetBytesSlope = 4916
	// #1478: 256-byte middle-sample tolerance covers event_seq digit and cyclic seed-name lengths.
	feedBudgetBytesLinearity = 256
	// #1478: real first-render elements have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetElementsLatest = 1692
	// #1478: real first-render forms have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetFormsLatest = 61
	// #1500: real first-render submits have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetSubmitLatest = 7
	// #1500: real first-render hx carriers have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetHXLatest = 11
	// #1500: real first-render htmx nodes have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetHTMXLatest = 72
	// #1500: real first-render bytes allow 5% (rounded up) for classes/text/translations; a count change (#1477) is a visible, justified budget change.
	feedBudgetBytesLatest = 275517
)
