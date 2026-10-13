package main

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type gatedShutdown struct{ expired, forced chan struct{} }

func (s gatedShutdown) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	close(s.expired)
	return ctx.Err()
}
func (s gatedShutdown) Close() error { close(s.forced); return nil }

type idleShutdownStream struct{ release chan struct{} }

func (s idleShutdownStream) EventsAfter(context.Context, kernel.ID, int64, int) ([]realtime.Event, error) {
	return nil, nil
}
func (s idleShutdownStream) MayReceive(context.Context, kernel.ID, string, realtime.Event) (bool, error) {
	return true, nil
}
func (s idleShutdownStream) Trigger(realtime.Event) bool { return false }
func (s idleShutdownStream) Recount(context.Context, realtime.Subscription) (realtime.Outgoing, error) {
	<-s.release // Deliberately ignores cancellation.
	return realtime.Outgoing{}, nil
}

func TestLifecycleWaitsForActualReturn(t *testing.T) {
	for _, blocked := range []string{"scheduler", "maintenance"} {
		t.Run(blocked, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hub, scheduler := realtime.NewHub(), realtime.NewRecountScheduler(1, 1)
				defer scheduler.Close()
				poolClosed, release := make(chan struct{}), make(chan struct{})
				l := newServiceLifecycle(t.Context(), hub, scheduler, func() { close(poolClosed) })
				server := gatedShutdown{make(chan struct{}), make(chan struct{})}
				l.servers = append(l.servers, server)
				workerRelease, workerJoined := make(chan struct{}, 1), make(chan struct{})
				var closed chan struct{}
				var shutdownErr error
				defer func() {
					close(release)
					close(workerRelease)
					if closed != nil {
						<-closed
					}
				}()
				if blocked == "scheduler" {
					workerRelease <- struct{}{}
				}
				l.workers = append(l.workers, func() { <-workerRelease; close(workerJoined) })
				ctx, unregister, err := hub.Register(l.ctx, realtime.Connection{Organization: kernel.ID{1}}, 1)
				if err != nil {
					t.Fatal(err)
				}
				defer unregister()
				source := idleShutdownStream{release}
				streamDone := make(chan error, 1)
				go func() {
					_, err := (realtime.Stream{Hub: hub, Events: source, Authorizer: source, Sidebar: source, Recounts: scheduler}).Run(ctx,
						realtime.Subscription{Organization: kernel.ID{1}, Interests: []realtime.Interest{realtime.InterestSidebar}}, 0, nil)
					streamDone <- err
				}()
				synctest.Wait()
				if hub.Waiting(kernel.ID{1}) != 1 || scheduler.Stats().RunningWorkers != 1 {
					t.Fatal("stream did not enter the combined wait with a running computation")
				}
				closed = make(chan struct{})
				go func() { shutdownErr = l.close(); close(closed) }()
				synctest.Wait()
				select {
				case err := <-streamDone:
					if !errors.Is(err, realtime.ErrShutdown) {
						t.Fatalf("idle stream cause = %v, want ErrShutdown", err)
					}
				default:
					t.Fatal("shutdown did not wake idle stream immediately")
				}
				select {
				case <-server.expired:
					t.Fatal("HTTP grace expired before idle stream woke")
				default:
				}
				if stats := scheduler.Stats(); !stats.Closed || stats.RunningWorkers != 1 {
					t.Fatalf("scheduler did not start closure alongside HTTP: %+v", stats)
				}
				// Let synctest advance to the HTTP deadline while the computation stays gated.
				<-server.expired
				synctest.Wait()
				select {
				case <-server.forced:
				default:
					t.Fatal("HTTP connections were not closed on grace expiry")
				}
				select {
				case <-poolClosed:
					t.Fatal("pool closed before computation returned")
				default:
				}
				release <- struct{}{}
				synctest.Wait()
				if blocked == "maintenance" {
					select {
					case <-poolClosed:
						t.Fatal("pool closed before maintenance worker joined")
					default:
					}
				}
				if blocked == "maintenance" {
					workerRelease <- struct{}{}
				}
				<-closed
				if !errors.Is(shutdownErr, context.DeadlineExceeded) {
					t.Fatalf("shutdown error = %v, want HTTP deadline", shutdownErr)
				}
				select {
				case <-workerJoined:
				default:
					t.Fatal("shutdown returned without joining maintenance worker")
				}
				select {
				case <-poolClosed:
				default:
					t.Fatal("shutdown returned without closing pool")
				}
			})
		})
	}
}

type recordingShutdown struct{ record func(string) }

func (s recordingShutdown) Shutdown(context.Context) error { s.record("http"); return nil }
func (s recordingShutdown) Close() error                   { s.record("forced"); return nil }

func TestServeFailureCleanupOrder(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	for _, failure := range []string{"handler", "metrics listener", "application listener", "normal shutdown"} {
		t.Run(failure, func(t *testing.T) {
			for key, value := range map[string]string{"RIBBITTO_ASYNC_RECOUNTS": "off", "RIBBITTO_DEV_ASSETS": "", "RIBBITTO_DEV_METRICS_ADDR": "", "RIBBITTO_ADDR": occupied.Addr().String(), "RIBBITTO_SIGNUP": "off", "RIBBITTO_SETUP_TOKEN": "", "RIBBITTO_EVENT_RETENTION": "168h"} {
				t.Setenv(key, value)
			}
			wantError := "address already in use"
			switch failure {
			case "handler":
				t.Setenv("RIBBITTO_DEV_ASSETS", t.TempDir()+"/missing")
				wantError = "reading stylesheet"
			case "metrics listener":
				t.Setenv("RIBBITTO_DEV_METRICS_ADDR", occupied.Addr().String())
				wantError = "RIBBITTO_DEV_METRICS_ADDR"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "normal shutdown" {
				t.Setenv("RIBBITTO_ADDR", "127.0.0.1:0")
			}
			var order []string
			record := func(step string) { order = append(order, step) }
			err := serveWithLifecycle(ctx, os.Getenv("RIBBITTO_TEST_DATABASE_URL"), func(ctx context.Context, hub *realtime.Hub, scheduler *realtime.RecountScheduler, closePool func()) *serviceLifecycle {
				l := newServiceLifecycle(ctx, hub, scheduler, func() { record("pool"); closePool() })
				if failure == "normal shutdown" {
					cancel()
				}
				l.stopStreams = func() { record("hub"); hub.CancelAll() }
				cancel := l.cancel
				l.cancel = func() { record("lifetime"); cancel() }
				l.servers = append(l.servers, recordingShutdown{record})
				l.workers = append(l.workers, func() { record("worker") })
				return l
			})
			if failure == "normal shutdown" && err != nil || failure != "normal shutdown" && (err == nil || !strings.Contains(err.Error(), wantError)) {
				t.Fatalf("serve error = %v, path %s", err, failure)
			}
			if want := []string{"hub", "lifetime", "http", "worker", "pool"}; !reflect.DeepEqual(order, want) {
				t.Fatalf("cleanup order = %v, want %v", order, want)
			}
		})
	}
}
