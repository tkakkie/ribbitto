package postgres

import (
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventKinds returns conversation's routers for wiring and tests until
// conversation registers its own kinds in step 4. Org registers through orgpg.
func EventKinds() realtime.Kinds {
	return realtime.Kinds{
		message.KindPosted:      message.RoutePosted,
		topic.KindMessagesMoved: topic.RouteMoved,
	}
}
