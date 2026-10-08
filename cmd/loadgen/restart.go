package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"syscall"
	"time"
)

type restartResult struct {
	SIGTERM, ReadyAt             time.Time
	RecoveryCursor               uint64
	ExitSeconds, ReadySeconds    float64
	ExitOverrun, IncompleteSetup bool
	Error                        string
	Old, New                     *snapshot       `json:",omitempty"`
	Recovery                     *recoveryResult `json:",omitempty"`
}

type recoveryResult struct {
	Reconnected, OutageRecovered, FullyCaughtUp   recoveryTimes
	LiveAgain                                     *recoveryTimes
	ResetStreams                                  int
	DeliveriesThroughRecoveryCursorAfterReconnect uint64 `json:"deliveries_through_recovery_cursor_after_reconnect"`
	AllDeliveriesAfterReconnect                   uint64 `json:"all_deliveries_after_reconnect"`
}

type recoveryTimes struct {
	P50Seconds, P95Seconds, MaxSeconds float64
	IncompleteStreams                  int
}

// The caller holds counts.mu at drain completion, before later worker updates.
func (r *restartResult) measureRecovery(records []streamRecord, initial, final uint64, drainEnd time.Time, deadline time.Duration, deliveries map[string]*post, rate int) {
	if r.IncompleteSetup || r.SIGTERM.IsZero() {
		return
	}
	r.Recovery = &recoveryResult{}
	var reconnects, recovered, caught, live []time.Duration
	recoveryEnd := minTime(drainEnd, r.SIGTERM.Add(deadline))
	for i := range records {
		rec := &records[i]
		if rec.Reset > 0 {
			r.Recovery.ResetStreams++
			continue
		}
		var reconnected time.Time
		for _, at := range rec.EstablishedAt {
			if at.After(r.SIGTERM) && !at.After(drainEnd) {
				reconnected = minTime(reconnected, at)
			}
		}
		if reconnected.IsZero() {
			continue
		}
		reconnects = append(reconnects, reconnected.Sub(r.SIGTERM))
		var liveAt time.Time
		for seq, arrivals := range rec.ArrivedAt {
			var p *post
			if receipt := rec.Sequences[seq]; receipt != nil {
				p = deliveries[receipt.Marker]
			}
			for _, at := range arrivals {
				if at.Before(reconnected) || at.After(drainEnd) {
					continue
				}
				// sent is the first attempt, even when a retry commits later.
				if p != nil && p.sent.After(reconnected) && at.After(reconnected) {
					liveAt = minTime(liveAt, at)
				}
				r.Recovery.AllDeliveriesAfterReconnect++
				if seq <= r.RecoveryCursor {
					r.Recovery.DeliveriesThroughRecoveryCursorAfterReconnect++
				}
			}
		}
		if !liveAt.IsZero() {
			live = append(live, liveAt.Sub(r.SIGTERM))
		}
		if at := rec.satisfiedAt(initial, r.RecoveryCursor, reconnected); !at.IsZero() && !at.After(recoveryEnd) {
			recovered = append(recovered, at.Sub(r.SIGTERM))
		}
		if at := rec.satisfiedAt(initial, final, reconnected); !at.IsZero() && !at.After(drainEnd) {
			caught = append(caught, at.Sub(r.SIGTERM))
		}
	}
	streams := len(records) - r.Recovery.ResetStreams
	r.Recovery.Reconnected = summarizeRecovery(reconnects, streams)
	r.Recovery.OutageRecovered = summarizeRecovery(recovered, streams)
	r.Recovery.FullyCaughtUp = summarizeRecovery(caught, streams)
	if rate > 0 {
		times := summarizeRecovery(live, streams)
		r.Recovery.LiveAgain = &times
	}
}

func (rec *streamRecord) satisfiedAt(initial, target uint64, reconnected time.Time) time.Time {
	if rec.Reset > 0 {
		return time.Time{}
	}
	if target <= initial {
		return reconnected
	}
	var satisfied time.Time
	for seq, arrivals := range rec.ArrivedAt {
		if seq >= target {
			for _, at := range arrivals {
				satisfied = minTime(satisfied, at)
			}
		}
	}
	if satisfied.IsZero() || satisfied.After(reconnected) {
		return satisfied
	}
	return reconnected
}

func minTime(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func summarizeRecovery(samples []time.Duration, streams int) recoveryTimes {
	r := recoveryTimes{IncompleteStreams: streams - len(samples)}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	if n := len(samples); n > 0 {
		r.P50Seconds, r.P95Seconds, r.MaxSeconds = samples[(n*50+99)/100-1].Seconds(), samples[(n*95+99)/100-1].Seconds(), samples[n-1].Seconds()
	}
	return r
}

type childServer struct {
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	readyAt time.Time
	cursor  uint64
}

func launchChild(ctx context.Context, path string, args []string, address, channel string, probe func(context.Context, *http.Client, string) (string, error), deadline, exitDeadline time.Duration) (*childServer, error) {
	host, port, err := net.SplitHostPort(address)
	n, portErr := strconv.Atoi(port)
	if err != nil || !net.ParseIP(host).IsLoopback() || portErr != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("server-addr requires a loopback IP and fixed port")
	}
	// Checking the bind also refuses listeners which do not answer HTTP.
	// Another process can claim the port after close, and readiness would then
	// probe it with a seeded test account's cookie. Load runs use a disposable,
	// loopback-only machine (maintainer, 2026-10-07), so a hostile local process
	// is outside this tool's threat model (maintainer, 2026-10-08).
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("child address already occupied: %w", err)
	}
	_ = listener.Close()
	child := &childServer{cmd: exec.Command(path, args...), done: make(chan struct{})}
	child.cmd.Stderr = os.Stderr
	child.cmd.Env = append(os.Environ(), "RIBBITTO_ADDR="+address)
	if err := child.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting child: %w", err)
	}
	go func() { child.err = child.cmd.Wait(); close(child.done) }()
	readyCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	tr, err := newTransport("http://"+address, "", &counts{})
	if err != nil {
		child.stop(exitDeadline)
		return nil, err
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	for {
		select {
		case <-child.done:
			return nil, fmt.Errorf("child exited before readiness: %v", child.err)
		default:
		}
		cursor, err := child.probe(readyCtx, client, "http://"+address+channel, probe)
		if err == nil {
			child.cursor, err = strconv.ParseUint(cursor, 10, 64)
			if err == nil {
				child.readyAt = time.Now()
				return child, nil
			}
		}
		if !waitRetry(readyCtx, 10*time.Millisecond, 0) {
			child.stop(exitDeadline)
			return nil, fmt.Errorf("child readiness deadline exceeded")
		}
	}
}

// A successful HTTP probe must still belong to a live child.
func (c *childServer) probe(ctx context.Context, client *http.Client, endpoint string, read func(context.Context, *http.Client, string) (string, error)) (string, error) {
	cursor, err := read(ctx, client, endpoint)
	if err == nil {
		select {
		case <-c.done:
			return "", fmt.Errorf("child exited at readiness: %v", c.err)
		default:
		}
	}
	return cursor, err
}

// Wait owns reaping; stop never looks up or signals any other process.
func (c *childServer) stop(deadline time.Duration) (float64, bool) {
	select {
	case <-c.done:
		return 0, false
	default:
	}
	start := time.Now()
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case <-c.done:
		return time.Since(start).Seconds(), false
	case <-timer.C:
		_ = c.cmd.Process.Kill()
		<-c.done
		return time.Since(start).Seconds(), true
	}
}

func caughtUp(records []streamRecord, initial, final uint64, terminated time.Time) bool {
	for i := range records {
		rec := &records[i]
		if rec.Reset > 0 {
			continue
		}
		reconnected, reached := false, final == initial
		for _, at := range rec.EstablishedAt {
			reconnected = reconnected || at.After(terminated)
		}
		for seq := range rec.ArrivedAt {
			reached = reached || seq >= final
		}
		if !reconnected || !reached {
			return false
		}
	}
	return true
}
