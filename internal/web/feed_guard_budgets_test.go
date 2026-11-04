package web

const (
	// #1457: shared menu/template move actions to the fixed part; exact counts.
	feedBudgetElementsFixed = 193
	// #1478: each message's DOM must not grow without justification.
	feedBudgetElementsSlope = 22
	// #1478: fixed forms add request-processing work; zero headroom.
	feedBudgetFormsFixed = 12
	// #1457: message forms are cloned only when opened; no initial slope.
	feedBudgetFormsSlope = 0
	// #1478: explicit submits participate in htmx processing; zero headroom.
	feedBudgetSubmitFixed = 7
	// #1500: message submits participate in htmx processing; zero headroom.
	feedBudgetSubmitSlope = 0
	// #1478: fixed hx carriers are an independent markup ratchet; zero headroom.
	feedBudgetHXFixed = 10
	// #1500: message hx carriers must not regrow; zero headroom.
	feedBudgetHXSlope = 0
	// #1478: htmx initializes fixed selected nodes including body; zero headroom.
	feedBudgetHTMXFixed = 21
	// #1457: no per-message forms are processed at initial load.
	feedBudgetHTMXSlope = 0
	// #1478: bytes allow 5% (rounded up) above the measured fixed part for classes/text/translations.
	feedBudgetBytesFixed = 24469
	// #1500: bytes allow 5% (rounded up) above the measured slope for classes/text/translations.
	feedBudgetBytesSlope = 3639
	// #1478: 256-byte middle-sample tolerance covers event_seq digit and cyclic seed-name lengths.
	feedBudgetBytesLinearity = 256
	// #1478, #1489: real first-render elements have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetElementsLatest = 1354
	// #1478: real first-render forms have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetFormsLatest = 12
	// #1500: real first-render submits have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetSubmitLatest = 7
	// #1500: real first-render hx carriers have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetHXLatest = 11
	// #1500: real first-render htmx nodes have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetHTMXLatest = 22
	// #1500: real first-render bytes allow 5% (rounded up) for classes/text/translations; a count change (#1477) is a visible, justified budget change.
	feedBudgetBytesLatest = 214299
)
