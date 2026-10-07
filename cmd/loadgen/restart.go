package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
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
	Old, New                     *snapshot `json:",omitempty"`
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
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("child address already occupied: %w", err)
	}
	_ = listener.Close()
	child := &childServer{cmd: exec.Command(path, args...), done: make(chan struct{})}
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
		cursor, err := probe(readyCtx, client, "http://"+address+channel)
		if err == nil {
			select {
			case <-child.done:
				return nil, fmt.Errorf("child exited at readiness: %v", child.err)
			default:
			}
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
	for _, rec := range records {
		reconnected, reached := false, final == initial
		for _, at := range rec.EstablishedAt {
			reconnected = reconnected || at.After(terminated)
		}
		for seq := range rec.ArrivedAt {
			reached = reached || seq >= final
		}
		if rec.Reset > 0 || !reconnected || !reached {
			return false
		}
	}
	return true
}
