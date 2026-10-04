package postgres

import (
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventKinds returns the publishers' Routers for the kinds written today, for
// wiring and tests, until each module registers its own (steps 3 and 4).
func EventKinds() realtime.Kinds {
	return realtime.Kinds{
		message.KindPosted:      message.RoutePosted,
		org.KindJoined:          org.RouteJoined,
		topic.KindMessagesMoved: topic.RouteMoved,
	}
}
