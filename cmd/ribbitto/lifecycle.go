package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

type shutdownServer interface {
	Shutdown(context.Context) error
	Close() error
}

// serviceLifecycle owns acquired resources even when startup only partly succeeds.
// Its context deliberately outlives the signal context: streams must receive
// ErrShutdown before service cancellation can race their request contexts.
type serviceLifecycle struct {
	ctx         context.Context
	cancel      context.CancelFunc
	stopStreams func()
	servers     []shutdownServer
	serverDone  chan struct{}
	serverJoins []func() error
	workers     []func()
	closePool   func()
	grace       time.Duration
}

func newServiceLifecycle(parent context.Context, hub *realtime.Hub, closePool func()) *serviceLifecycle {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	return &serviceLifecycle{ctx: ctx, cancel: cancel, stopStreams: hub.CancelAll,
		closePool: closePool, grace: 10 * time.Second, serverDone: make(chan struct{}, 1)}
}

func (l *serviceLifecycle) close() error {
	l.stopStreams()
	l.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), l.grace)
	defer cancel()
	drained := make(chan error, len(l.servers))
	for _, server := range l.servers {
		go func() {
			err := server.Shutdown(ctx)
			if err != nil {
				err = errors.Join(err, server.Close())
			}
			drained <- err
		}()
	}
	var err error
	for range l.servers {
		err = errors.Join(err, <-drained)
	}
	for _, stop := range l.workers {
		stop()
	}
	for _, join := range l.serverJoins {
		if result := join(); !errors.Is(result, http.ErrServerClosed) {
			err = errors.Join(err, result)
		}
	}
	l.closePool()
	return err
}

func (l *serviceLifecycle) startServer(server shutdownServer, serve func() error) {
	l.servers = append(l.servers, server)
	done := make(chan struct{})
	var result error
	l.serverJoins = append(l.serverJoins, func() error {
		<-done
		return result
	})
	go func() {
		defer close(done)
		result = serve()
		// This only wakes shutdown; the join collects every result even if a
		// signal wins the select. Coalescing wakes cannot block a server's exit.
		select {
		case l.serverDone <- struct{}{}:
		default:
		}
	}()
}
