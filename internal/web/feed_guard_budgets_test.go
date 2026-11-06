package web

const (
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetElementsFixed = 211
	// #1478: each message's DOM must not grow without justification.
	feedBudgetElementsSlope = 22
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetFormsFixed = 13
	// #1457: message forms are cloned only when opened; no initial slope.
	feedBudgetFormsSlope = 0
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetSubmitFixed = 8
	// #1500: message submits participate in htmx processing; zero headroom.
	feedBudgetSubmitSlope = 0
	// #1478: fixed hx carriers are an independent markup ratchet; zero headroom.
	feedBudgetHXFixed = 10
	// #1500: message hx carriers must not regrow; zero headroom.
	feedBudgetHXSlope = 0
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetHTMXFixed = 23
	// #1457: no per-message forms are processed at initial load.
	feedBudgetHTMXSlope = 0
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixed bytes.
	feedBudgetBytesFixed = 25732
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic byte slope.
	feedBudgetBytesSlope = 3467
	// #1478: 256-byte middle-sample tolerance covers event_seq digit and cyclic seed-name lengths.
	feedBudgetBytesLinearity = 256
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetElementsLatest = 1372
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetFormsLatest = 13
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetSubmitLatest = 8
	// #1500: real first-render hx carriers have zero headroom; a count change (#1477) is a visible, justified budget change.
	feedBudgetHXLatest = 11
	// #2183: https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6672846682 approves the resolved-topic fixture.
	feedBudgetHTMXLatest = 24
	// #1500: real first-render bytes allow 5% (rounded up) for classes/text/translations; a count change (#1477) is a visible, justified budget change.
	feedBudgetBytesLatest = 214299
)
