// Command loadgen exercises a disposable loopback server and compares run files.
package main

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type fixture struct {
	Slug     string                      `json:"organization_slug"`
	Channels []string                    `json:"channel_ids"`
	Accounts []struct{ Tokens []string } `json:"accounts"`
}

// runFile follows the v1 contract defined in load-client.md.
type runFile struct {
	Header   runHeader         `json:"header"`
	Streams  []streamRecord    `json:"streams"`
	Messages []expectedMessage `json:"messages,omitempty"`
}
type expectedMessage struct {
	Sequence uint64 `json:"sequence"`
	Marker   string `json:"marker"`
}
type runHeader struct {
	Version          int    `json:"version"`
	Kind             string `json:"kind"`
	OrganizationSlug string `json:"organization_slug"`
	ChannelID        string `json:"channel_id"`
	InitialCursor    uint64 `json:"initial_cursor"`
	FinalWatermark   uint64 `json:"final_watermark"`
}
type streamRecord struct {
	open          bool
	EstablishedAt []time.Time            `json:"-"`
	ArrivedAt     map[uint64][]time.Time `json:"-"`
	Index         int                    `json:"index"`
	Sequences     map[uint64]*receipt    `json:"sequences"`
	Reset         uint64                 `json:"reset"`
	Reconnects    connectionCounts       `json:"reconnects"`
}
type receipt struct {
	Arrivals uint64 `json:"arrivals"`
	Marker   string `json:"marker"`
}
type connectionCounts struct {
	Established uint64 `json:"established"`
	Unavailable uint64 `json:"503"`
	Refused     uint64 `json:"refused"`
	Other       uint64 `json:"other"`
}
type reconnectModel struct {
	Delay, Jitter time.Duration
	PostAttempts  int
	Attempts      connectionCounts
}

// maxStreams covers #216's idle steps (10k, 20k …) with room to spare and
// keeps the run finite.
const maxStreams = 100_000

const (
	connectionEstablished = iota
	connectionUnavailable
	connectionRefused
	connectionOther
)

type counts struct {
	postAttempts                                                                    atomic.Uint64
	connectionAttempts                                                              [4]atomic.Uint64
	sources                                                                         []*net.TCPAddr
	nextSource                                                                      atomic.Uint64
	dialFailures                                                                    [5]atomic.Uint64
	renders                                                                         map[uint64]int
	established, limited, shutdown, reset, failed, posts, postFailed, tcp, received atomic.Uint64
	http2                                                                           atomic.Bool
	mu                                                                              sync.Mutex
	deliveries                                                                      map[string]*post
	frozen, handover                                                                bool
	deadline                                                                        time.Time
	markers                                                                         *regexp.Regexp
}
type post struct {
	sent       time.Time
	answered   bool
	latencies  []time.Duration
	duplicates uint64
}
type result struct {
	Restart                                                                                                             *restartResult  `json:",omitempty"`
	Reconnect                                                                                                           *reconnectModel `json:",omitempty"`
	Cursor, StreamsAttempted, Established, Refused429, Refused503, Reset, Failed, TCPConnections                        uint64
	Scheduled, Sent, Answered200, PostFailed, Missed, Expected, Received, Missing, Duplicated, PostAttemptsMade         uint64
	Rate, DialConcurrency                                                                                               int
	DurationSeconds, SetupLimitSeconds, DrainLimitSeconds, SetupSeconds, ObservationSeconds, DrainSeconds, AchievedRate float64
	P50MS, P95MS, MaxMS                                                                                                 float64
	CursorOverride, CursorValid, HTTP2, Slow, DeliveryMissing, RequestFailed                                            bool
	BodyLength                                                                                                          int
	BodyEscape                                                                                                          bool
	Server                                                                                                              *serverMetrics
	Generator                                                                                                           struct {
		NOFILESoft, NOFILEHard uint64
		Goroutines             int
		HeapInuseBytes         uint64
	}
	DialFailures struct{ TooManyOpenFiles, AddressUnavailableOrPortsExhausted, ConnectionRefused, Timeout, Other uint64 }
	Renders      struct {
		Count, MinBytes, MaxBytes, TotalBytes int
		MeanBytes                             float64
		SizesBytes                            []int
	}
	Verdict string
}

type snapshot struct {
	Streams struct {
		Open int64 `json:"open"`
	} `json:"streams"`
	Runtime struct {
		Goroutines     int64 `json:"goroutines"`
		HeapInuseBytes int64 `json:"heap_inuse_bytes"`
	} `json:"runtime"`
	Pool struct {
		TotalConns         int64 `json:"total_conns"`
		MaxConns           int64 `json:"max_conns"`
		EmptyAcquireCount  int64 `json:"empty_acquire_count"`
		EmptyAcquireWaitNS int64 `json:"empty_acquire_wait_ns"`
	} `json:"pool"`
	Database struct {
		Queries           int64 `json:"queries"`
		TransactionsBegun int64 `json:"transactions_begun"`
	} `json:"database"`
}
type serverMetrics struct {
	Before, After, Closed snapshot
	Delta                 struct{ Queries, TransactionsBegun, EmptyAcquireCount, EmptyAcquireWaitNS int64 }
}

func readSnapshot(t transport, dst *snapshot) error {
	client := &http.Client{Transport: t, Timeout: 5 * time.Second}
	r, err := client.Get(t.origin.String() + "/metrics")
	if err != nil {
		return fmt.Errorf("reading metrics: %w", err)
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusOK {
		return fmt.Errorf("metrics HTTP status %d", r.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst); err != nil {
		return fmt.Errorf("decoding metrics: %w", err)
	}
	return nil
}

func dialFailure(err error) int {
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return 0
	case errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EADDRINUSE):
		return 1
	case errors.Is(err, syscall.ECONNREFUSED):
		return 2
	case errors.As(err, &netErr) && netErr.Timeout():
		return 3
	default:
		return 4
	}
}

func paddedBody(marker string, length int, escape bool) string {
	char := "x"
	if escape {
		char = "&"
	}
	return marker + strings.Repeat(char, max(0, length-len(marker)))
}

func (c *counts) receive(seq uint64, data string, seen map[*post]bool) {
	arrival := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen || (!c.deadline.IsZero() && arrival.After(c.deadline)) {
		return
	}
	c.receiveLocked(seq, data, seen, arrival)
}

func (c *counts) receiveLocked(seq uint64, data string, seen map[*post]bool, arrival time.Time) {
	markers := c.markers.FindAllString(data, -1)
	sort.Strings(markers)
	if seq > 0 {
		if c.renders == nil {
			c.renders = make(map[uint64]int)
		}
		if _, ok := c.renders[seq]; !ok {
			c.renders[seq] = len(data)
		}
	}
	for i, marker := range markers {
		if i > 0 && marker == markers[i-1] {
			continue
		}
		if p := c.deliveries[marker]; p != nil {
			if seen[p] {
				p.duplicates++
			} else {
				p.latencies = append(p.latencies, arrival.Sub(p.sent))
				seen[p] = true
				if p.answered {
					c.received.Add(1)
				}
			}
		}
	}
}

func (r *result) verdict(threshold time.Duration) {
	r.Slow, r.DeliveryMissing = r.P95MS > float64(threshold)/float64(time.Millisecond), r.Missing > 0
	r.RequestFailed = r.Refused429+r.Refused503+r.Reset+r.Failed+r.PostFailed > 0
	r.Verdict = "pass"
	if r.Slow || r.DeliveryMissing || r.RequestFailed {
		r.Verdict = "fail"
	}
	if r.Missed > 0 || r.Sent < r.Scheduled {
		r.Verdict = "underloaded"
	}
}

type transport struct {
	*http.Transport
	origin *url.URL
}

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	// The client leaves Host empty on a redirect; it then means URL.Host.
	if r.URL.Scheme != t.origin.Scheme || r.URL.Host != t.origin.Host || r.URL.User != nil || (r.Host != "" && r.Host != r.URL.Host) {
		return nil, fmt.Errorf("request leaves target origin")
	}
	return t.Transport.RoundTrip(r)
}

func loopback(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("refusing non-loopback connection")
	}
	return nil
}

func newTransport(target, ca string, c *counts) (transport, error) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback()) {
		return transport{}, fmt.Errorf("target must be a loopback HTTP(S) origin")
	}
	roots := x509.NewCertPool()
	if u.Scheme == "https" {
		pem, err := os.ReadFile(ca)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return transport{}, fmt.Errorf("HTTPS requires -ca pointing to Caddy's local CA PEM")
		}
	}
	// Control sees each resolved IP immediately before connect, including retries.
	t := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: 5 * time.Second, Control: loopback}
		if len(c.sources) > 0 {
			dialer.LocalAddr = c.sources[(c.nextSource.Add(1)-1)%uint64(len(c.sources))]
		}
		conn, err := dialer.DialContext(ctx, network, address)
		if err == nil {
			c.tcp.Add(1)
		} else if ctx.Err() == nil {
			c.dialFailures[dialFailure(err)].Add(1)
		}
		return conn, err
	}
	return transport{t, u}, nil
}

func request(ctx context.Context, client *http.Client, method, endpoint, token, cursor string) (*http.Response, error) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(url.Values{"body": {cursor}}.Encode())
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	r.AddCookie(&http.Cookie{Name: "__Host-session", Value: token})
	if method == http.MethodGet && strings.HasSuffix(endpoint, "/events") {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Last-Event-ID", cursor)
	} else if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", r.URL.Scheme+"://"+r.URL.Host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("HX-Request", "true")
	}
	return client.Do(r)
}

// A reset is dispatched only at a complete SSE event with a data field.
// The cursor advances only at the blank line, never on a partial payload.
func readReset(body io.Reader, receive func(uint64, string), complete func(uint64, string, string)) bool {
	s := bufio.NewScanner(body)
	s.Buffer(make([]byte, 4096), 1<<20)
	event, data := "", false
	var seq uint64
	var payload strings.Builder
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if event == "reset" && data {
				return true
			}
			if data && receive != nil {
				renderSeq := uint64(0)
				if event == "message" {
					renderSeq = seq
				}
				receive(renderSeq, strings.TrimSuffix(payload.String(), "\n"))
			}
			// The stream callback accounts for delivery and records receipts in one critical section so drain cannot freeze between them.
			if data && event != "reset" && complete != nil {
				complete(seq, event, strings.TrimSuffix(payload.String(), "\n"))
			}
			payload.Reset()
			event, data, seq = "", false, 0
		}
		field, value, _ := strings.Cut(line, ":")
		if field == "id" {
			seq, _ = strconv.ParseUint(strings.TrimPrefix(value, " "), 10, 64)
		}
		if field == "event" {
			event = strings.TrimPrefix(value, " ")
		}
		if field == "data" {
			payload.WriteString(strings.TrimPrefix(value, " "))
			payload.WriteByte('\n')
		}
		data = data || field == "data"
	}
	return false
}

func streamOnce(ctx context.Context, client *http.Client, endpoint, token string, cursor *string, c *counts, setup context.Context, opened, established func(), seen map[*post]bool, rec *streamRecord, reconnect bool) (outcome int, retry bool) {
	defer func() {
		if ctx.Err() == nil || outcome == connectionEstablished {
			c.connectionAttempts[outcome].Add(1)
			if reconnect {
				c.mu.Lock()
				defer c.mu.Unlock()
				fields := []*uint64{&rec.Reconnects.Established, &rec.Reconnects.Unavailable, &rec.Reconnects.Refused, &rec.Reconnects.Other}
				*fields[outcome]++
			}
		}
	}()
	notify := sync.OnceFunc(opened)
	defer notify()
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(setup, cancel)
	r, err := request(dialCtx, client, http.MethodGet, endpoint, token, *cursor)
	stop()
	if err != nil {
		// The harness closing the run is not a failed stream.
		if ctx.Err() == nil {
			c.failed.Add(1)
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return connectionRefused, true
		}
		return connectionOther, true
	}
	defer func() { _ = r.Body.Close() }()
	if r.ProtoMajor == 2 {
		c.http2.Store(true)
	}
	switch r.StatusCode {
	case http.StatusTooManyRequests:
		c.limited.Add(1)
		return connectionOther, false
	case http.StatusServiceUnavailable:
		c.shutdown.Add(1)
		return connectionUnavailable, true
	case http.StatusOK:
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
			c.failed.Add(1)
			return connectionOther, false
		}
		c.mu.Lock()
		rec.open = true
		if c.handover {
			rec.EstablishedAt = append(rec.EstablishedAt, time.Now())
		}
		c.mu.Unlock()
		defer func() { c.mu.Lock(); rec.open = false; c.mu.Unlock() }()
		established()
		notify()
		if readReset(r.Body, nil, func(seq uint64, event, data string) {
			arrival := time.Now()
			c.mu.Lock()
			defer c.mu.Unlock()
			renderSeq := uint64(0)
			if event == "message" {
				renderSeq = seq
			}
			if seq != 0 {
				*cursor = strconv.FormatUint(seq, 10)
			}
			if c.frozen || (!c.deadline.IsZero() && arrival.After(c.deadline)) {
				return
			}
			c.receiveLocked(renderSeq, data, seen, arrival)
			if seq == 0 {
				return
			}
			if c.handover {
				if rec.ArrivedAt == nil {
					rec.ArrivedAt = make(map[uint64][]time.Time)
				}
				rec.ArrivedAt[seq] = append(rec.ArrivedAt[seq], arrival)
			}
			if event == "message" {
				if rec.Sequences[seq] == nil {
					rec.Sequences[seq] = &receipt{Marker: c.markers.FindString(data)}
				}
				rec.Sequences[seq].Arrivals++
			}
		}) {
			c.reset.Add(1)
			c.mu.Lock()
			rec.Reset++
			c.mu.Unlock()
			return connectionEstablished, false
		} else if ctx.Err() == nil {
			c.failed.Add(1)
		}
		return connectionEstablished, true
	default:
		c.failed.Add(1)
	}
	return connectionOther, r.StatusCode >= 500 && r.StatusCode < 600
}

func retryDelay(delay, jitter time.Duration) time.Duration {
	if jitter > 0 {
		delay += time.Duration(mathrand.Int64N(int64(jitter) + 1))
	}
	return delay
}

func waitRetry(ctx context.Context, delay, jitter time.Duration) bool {
	timer := time.NewTimer(retryDelay(delay, jitter))
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

func stream(ctx context.Context, client *http.Client, endpoint, token, cursor string, c *counts, setup context.Context, slots chan struct{}, opened func(), model *reconnectModel, rec *streamRecord) {
	seen := make(map[*post]bool)
	established := sync.OnceFunc(func() { c.established.Add(1) })
	for attempt := 0; ; attempt++ {
		_, retry := streamOnce(ctx, client, endpoint, token, &cursor, c, setup, opened, established, seen, rec, attempt > 0)
		if ctx.Err() != nil {
			return
		}

		if model == nil || !retry || !waitRetry(ctx, model.Delay, model.Jitter) {
			return
		}
		setup = ctx
		select {
		case slots <- struct{}{}:
			opened = func() { <-slots }
		case <-ctx.Done():
			return
		}
	}
}

func sendPost(ctx context.Context, client *http.Client, endpoint, token, body string, c *counts, model *reconnectModel) bool {
	attempts := 1
	if model != nil {
		attempts = model.PostAttempts
	}
	for i := 0; i < attempts; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		c.postAttempts.Add(1)
		response, err := request(attemptCtx, client, http.MethodPost, endpoint, token, body)
		status := 0
		if err == nil {
			status = response.StatusCode
			if response.ProtoMajor == 2 {
				c.http2.Store(true)
			}
			// A received status is final even if draining the body fails.
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
		}
		cancel()
		if status == http.StatusOK {
			return true
		}
		retry := status == 0 || (status >= 500 && status < 600)
		if i+1 == attempts || !retry || !waitRetry(ctx, model.Delay, 0) {
			return false
		}
	}
	return false
}

func run(args []string, out io.Writer) (runErr error) {
	flags := flag.NewFlagSet("loadgen (development only)", flag.ContinueOnError)
	compare := flags.String("compare", "", "compare receipt run file with positional expected file")
	server := flags.String("server", "", "child executable (environment passed through)")
	address := flags.String("server-addr", "127.0.0.1:8080", "child loopback IP:port; overrides RIBBITTO_ADDR")
	var serverArgs []string
	flags.Func("server-arg", "child argument (repeatable)", func(v string) error { serverArgs = append(serverArgs, v); return nil })
	restartAfter := flags.Duration("restart-after", 0, "restart after setup; requires -server (0 disables)")
	startDeadline := flags.Duration("start-deadline", 30*time.Second, "child readiness deadline (0, 5m]")
	exitDeadline := flags.Duration("exit-deadline", 10*time.Second, "child exit deadline (0, 5m]")
	reconnect := flags.Bool("reconnect", false, "enable the client's fixed-delay reconnect model")
	delay := flags.Duration("reconnect-delay", 250*time.Millisecond, "fixed retry delay [0, 10s]")
	jitter := flags.Duration("reconnect-jitter", 250*time.Millisecond, "random stream retry jitter [0, 10s]")
	attempts := flags.Int("post-attempts", 3, "POST attempts with -reconnect [1, 10]")
	receipts := flags.String("receipts", "", "exclusive receipt run file (0600)")
	metrics := flags.String("metrics", "", "loopback HTTP metrics origin")
	source := flags.String("source", "", "up to 64 comma-separated loopback IPv4 sources")
	bodyLength := flags.Int("body-length", 0, "padded body length [0, 4000]; marker is never truncated")
	bodyEscape := flags.Bool("body-escape", false, "pad with HTML-escaped characters")
	target := flags.String("target", "http://127.0.0.1:8080", "loopback origin")
	file := flags.String("tokens", "", "seed credential file (never printed)")
	ca := flags.String("ca", "", "Caddy local CA PEM; required for HTTPS")
	duration := flags.Duration("duration", 10*time.Second, "run duration (0, 10m]")
	streams := flags.Int("streams", 1, "stream attempts [1, 100000]")
	rate := flags.Int("rate", 0, "total posts per second [0, 100]")
	cursor := flags.String("cursor", "", "override the channel page cursor")
	drain := flags.Duration("drain", 30*time.Second, "delivery drain (0, 5m]")
	setup := flags.Duration("setup", 30*time.Second, "stream setup deadline (0, 5m]")
	dials := flags.Int("dial-concurrency", 64, "concurrent stream attempts [1, 100000]")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("invalid flags")
	}
	// Whether -compare was given decides the mode, not its value: an empty
	// -compare= must be refused, never fall through to a live run.
	comparing := false
	flags.Visit(func(f *flag.Flag) { comparing = comparing || f.Name == "compare" })
	if comparing {
		flags.Visit(func(f *flag.Flag) {
			if f.Name != "compare" {
				runErr = fmt.Errorf("-compare cannot be combined with -%s", f.Name)
			}
		})
		if runErr != nil {
			return runErr
		}
		if *compare == "" || flags.NArg() != 1 {
			return fmt.Errorf("-compare requires receipts and expected files")
		}
		return compareFiles(*compare, flags.Arg(0), out)
	}
	if *delay < 0 || *delay > 10*time.Second || *jitter < 0 || *jitter > 10*time.Second || *attempts < 1 || *attempts > 10 || flags.NArg() != 0 || *bodyLength < 0 || *bodyLength > 4000 || *duration <= 0 || *duration > 10*time.Minute || *streams < 1 || *streams > maxStreams || *rate < 0 || *rate > 100 || *drain <= 0 || *drain > 5*time.Minute || *setup <= 0 || *setup > 5*time.Minute || *dials < 1 || *dials > maxStreams {
		return fmt.Errorf("flag outside finite limits")
	}
	if *restartAfter < 0 || *restartAfter >= *duration || *startDeadline <= 0 || *startDeadline > 5*time.Minute || *exitDeadline <= 0 || *exitDeadline > 5*time.Minute {
		return fmt.Errorf("flag outside finite limits")
	}
	if *restartAfter > 0 {
		if *server == "" {
			return fmt.Errorf("restart-after requires -server")
		}
		*reconnect = true
	}
	flags.Visit(func(f *flag.Flag) {
		if (f.Name == "server-addr" || f.Name == "server-arg" || f.Name == "start-deadline" || f.Name == "exit-deadline") && *server == "" {
			runErr = fmt.Errorf("%s requires -server", f.Name)
		}
		if (f.Name == "post-attempts" || f.Name == "reconnect-delay" || f.Name == "reconnect-jitter") && !*reconnect {
			runErr = fmt.Errorf("%s requires -reconnect", f.Name)
		}
	})
	if runErr != nil {
		return runErr
	}
	c := &counts{handover: *restartAfter > 0}
	if *source != "" {
		for _, address := range strings.Split(*source, ",") {
			ip := net.ParseIP(address)
			if ip == nil || ip.To4() == nil || !ip.IsLoopback() || strings.Contains(address, ":") || len(c.sources) == 64 {
				return fmt.Errorf("source requires at most 64 loopback IPv4 addresses")
			}
			c.sources = append(c.sources, &net.TCPAddr{IP: ip})
		}
	}
	var mt transport
	if *metrics != "" {
		var err error
		mt, err = newTransport(*metrics, "", &counts{})
		if err != nil || mt.origin.Scheme != "http" {
			return fmt.Errorf("metrics must be a loopback HTTP origin")
		}
		defer mt.CloseIdleConnections()
		mt.origin.Path = ""
	}
	t, err := newTransport(*target, *ca, c)
	if err != nil {
		return err
	}
	defer t.CloseIdleConnections()
	f, err := os.Open(*file)
	if err != nil {
		return fmt.Errorf("cannot open credential file")
	}
	defer func() { _ = f.Close() }()
	var data fixture
	if json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&data) != nil || data.Slug == "" || len(data.Channels) == 0 || len(data.Accounts) == 0 {
		return fmt.Errorf("invalid credential file")
	}
	for _, a := range data.Accounts {
		if len(a.Tokens) == 0 {
			return fmt.Errorf("missing account tokens")
		}
		for _, token := range a.Tokens {
			if token == "" || (&http.Cookie{Name: "__Host-session", Value: token}).Valid() != nil {
				return fmt.Errorf("invalid session cookie")
			}
		}
	}
	r := result{BodyLength: *bodyLength, BodyEscape: *bodyEscape}
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		return fmt.Errorf("reading file limits: %w", err)
	}
	r.Generator.NOFILESoft, r.Generator.NOFILEHard = limits.Cur, limits.Max
	client := &http.Client{Transport: t}
	streamCtx, stopStreams := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopStreams()
	endpoint := t.origin.Scheme + "://" + t.origin.Host + "/organizations/" + url.PathEscape(data.Slug) + "/channels/" + url.PathEscape(data.Channels[0])
	tokenAt := func(i int) string {
		a := data.Accounts[i%len(data.Accounts)]
		return a.Tokens[(i/len(data.Accounts))%len(a.Tokens)]
	}
	explicitCursor := false
	flags.Visit(func(f *flag.Flag) { explicitCursor = explicitCursor || f.Name == "cursor" })
	readCursor := func(ctx context.Context, client *http.Client, endpoint string) (string, error) {
		page, err := request(ctx, client, http.MethodGet, endpoint, tokenAt(0), "")
		if err != nil {
			return "", fmt.Errorf("cannot read channel cursor")
		}
		html, err := io.ReadAll(io.LimitReader(page.Body, 16<<20))
		_ = page.Body.Close()
		if page.ProtoMajor == 2 {
			c.http2.Store(true)
		}
		match := regexp.MustCompile(`sse-connect="[^"\n]*/events\?after=([0-9]+)"`).FindSubmatch(html)
		if err != nil || page.StatusCode != http.StatusOK || len(match) != 2 {
			return "", fmt.Errorf("invalid channel cursor")
		}
		return string(match[1]), nil
	}
	readTargetCursor := func() (string, error) {
		ctx, cancel := context.WithTimeout(streamCtx, 10*time.Second)
		defer cancel()
		return readCursor(ctx, client, endpoint)
	}
	var child *childServer
	defer func() {
		if child != nil {
			child.stop(*exitDeadline)
		}
	}()
	launch := func() error {
		var launchErr error
		child, launchErr = launchChild(streamCtx, *server, serverArgs, *address, strings.TrimPrefix(endpoint, t.origin.Scheme+"://"+t.origin.Host), readCursor, *startDeadline, *exitDeadline)
		if launchErr != nil {
			return launchErr
		}
		ctx, cancel := context.WithTimeout(streamCtx, *startDeadline)
		defer cancel()
		for {
			if _, err := readCursor(ctx, client, endpoint); err == nil {
				return nil
			}
			if !waitRetry(ctx, 10*time.Millisecond, 0) {
				return fmt.Errorf("target readiness deadline exceeded")
			}
		}
	}
	if *server != "" {
		if err := launch(); err != nil {
			return err
		}
	}
	if *metrics != "" && *restartAfter == 0 {
		r.Server = &serverMetrics{}
		if err := readSnapshot(mt, &r.Server.Before); err != nil {
			return err
		}
	}
	if !explicitCursor {
		*cursor, err = readTargetCursor()
		if err != nil {
			return err
		}
	}
	prefix := "loadgen" + cryptorand.Text()
	c.markers, c.deliveries = regexp.MustCompile(prefix+`[0-9]+Z`), make(map[string]*post)
	cursorNumber, cursorErr := strconv.ParseUint(*cursor, 10, 64)
	r.Cursor, r.CursorValid, r.CursorOverride = cursorNumber, cursorErr == nil, explicitCursor
	var receiptFile *os.File
	if *receipts != "" {
		if cursorErr != nil {
			return fmt.Errorf("receipts require a numeric cursor")
		}
		receiptFile, err = os.OpenFile(*receipts, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("creating receipts: %w", err)
		}
		defer func() {
			_ = receiptFile.Close()
			if runErr != nil {
				_ = os.Remove(*receipts)
			}
		}()
	}
	records := make([]streamRecord, *streams)
	for i := range records {
		records[i] = streamRecord{Index: i, Sequences: make(map[uint64]*receipt)}
	}
	if *reconnect {
		r.Reconnect = &reconnectModel{Delay: *delay, Jitter: *jitter, PostAttempts: *attempts}
	}
	r.StreamsAttempted, r.Rate, r.DialConcurrency = uint64(*streams), *rate, *dials
	r.DurationSeconds, r.SetupLimitSeconds, r.DrainLimitSeconds = duration.Seconds(), setup.Seconds(), drain.Seconds()
	start := time.Now()
	setupCtx, stopSetup := context.WithTimeout(streamCtx, *setup)
	defer stopSetup()
	slots := make(chan struct{}, *dials)
	var wg, ready sync.WaitGroup
	ready.Add(*streams)
	for i := range *streams {
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				stream(streamCtx, client, endpoint+"/events", tokenAt(i), *cursor, c, setupCtx, slots, func() { <-slots; ready.Done() }, r.Reconnect, &records[i])
			case <-setupCtx.Done():
				c.failed.Add(1)
				ready.Done()
			}
		})
	}
	ready.Wait()
	stopSetup()
	r.SetupSeconds = time.Since(start).Seconds()
	start = time.Now()
	var storm sync.WaitGroup
	if *restartAfter > 0 {
		r.Restart = &restartResult{}
		c.mu.Lock()
		for i := range records {
			r.Restart.IncompleteSetup = r.Restart.IncompleteSetup || !records[i].open
		}
		c.mu.Unlock()
		storm.Go(func() {
			if r.Restart.IncompleteSetup || !waitRetry(streamCtx, *restartAfter, 0) {
				return
			}
			select {
			case <-child.done:
				r.Restart.Error = "child exited before SIGTERM"
				return
			default:
			}
			c.mu.Lock()
			for i := range records {
				if !records[i].open {
					r.Restart.IncompleteSetup = true
				}
			}
			c.mu.Unlock()
			if r.Restart.IncompleteSetup {
				return
			}
			if *metrics != "" {
				r.Restart.Old = &snapshot{}
				if err := readSnapshot(mt, r.Restart.Old); err != nil {
					r.Restart.Error = err.Error()
					return
				}
			}
			select {
			case <-child.done:
				r.Restart.Error = "child exited before SIGTERM"
				return
			default:
			}
			r.Restart.SIGTERM = time.Now()
			r.Restart.ExitSeconds, r.Restart.ExitOverrun = child.stop(*exitDeadline)
			if err := launch(); err != nil {
				r.Restart.Error = err.Error()
				return
			}
			r.Restart.ReadyAt, r.Restart.RecoveryCursor = child.readyAt, child.cursor
			r.Restart.ReadySeconds = time.Since(r.Restart.SIGTERM).Seconds()
		})
	}
	var posts sync.WaitGroup
	inflight := make(chan struct{}, *rate)
	if *rate > 0 {
		r.Scheduled = (uint64(*duration)*uint64(*rate) + uint64(time.Second) - 1) / uint64(time.Second)
	}
	for i := uint64(0); i < r.Scheduled; i++ {
		if !waitRetry(streamCtx, max(0, time.Until(start.Add(time.Duration(i)*time.Second/time.Duration(*rate)))), 0) {
			break
		}
		select {
		case inflight <- struct{}{}:
			if time.Since(start) >= *duration {
				<-inflight
				r.Missed++
				continue
			}
		default:
			r.Missed++
			continue
		}
		marker := prefix + strconv.FormatUint(i, 10) + "Z"
		// The marker is registered before the request so that a delivery
		// arriving before the POST's response is kept.
		p := &post{}
		c.mu.Lock()
		c.deliveries[marker] = p
		c.mu.Unlock()
		r.Sent++
		posts.Go(func() {
			defer func() { <-inflight }()
			body := paddedBody(marker, *bodyLength, *bodyEscape)
			// Post-to-receipt latency starts here, not when the slot was
			// scheduled, so the client's own goroutine scheduling is excluded.
			c.mu.Lock()
			p.sent = time.Now()
			c.mu.Unlock()
			if sendPost(streamCtx, client, endpoint, tokenAt(int(i)), body, c, r.Reconnect) {
				c.mu.Lock()
				p.answered = true
				c.received.Add(uint64(len(p.latencies)))
				c.mu.Unlock()
				c.posts.Add(1)
			} else {
				c.postFailed.Add(1)
			}
		})
	}
	_ = waitRetry(streamCtx, max(0, time.Until(start.Add(*duration))), 0)
	r.ObservationSeconds = time.Since(start).Seconds()
	r.Generator.Goroutines = runtime.NumGoroutine()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	r.Generator.HeapInuseBytes = memory.HeapInuse
	posts.Wait() // Include commits with a lost response in the watermark.
	storm.Wait()
	defer func() { stopStreams(); wg.Wait() }()
	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	if r.Restart != nil && r.Restart.Error != "" {
		return fmt.Errorf("restart: %s", r.Restart.Error)
	}
	var final uint64
	if receiptFile != nil || r.Restart != nil {
		watermark, err := readTargetCursor()
		if err != nil {
			return err
		}
		final, err = strconv.ParseUint(watermark, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid final watermark")
		}
	}
	start = time.Now()
	r.Expected = c.established.Load() * c.posts.Load()
	if *reconnect {
		r.Expected = uint64(*streams) * c.posts.Load()
	}
	c.mu.Lock()
	c.deadline = start.Add(*drain)
	c.mu.Unlock()
	for {
		complete := c.received.Load() >= r.Expected
		if r.Restart != nil && !r.Restart.IncompleteSetup {
			c.mu.Lock()
			complete = !r.Restart.SIGTERM.IsZero() && r.Restart.Error == "" && caughtUp(records, cursorNumber, final, r.Restart.SIGTERM)
			c.mu.Unlock()
		}
		if complete || time.Since(start) >= *drain || streamCtx.Err() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := streamCtx.Err(); err != nil {
		return fmt.Errorf("interrupted during drain: %w", err)
	}
	c.mu.Lock()
	c.frozen = true
	c.mu.Unlock()
	r.DrainSeconds = time.Since(start).Seconds()
	if r.Restart != nil && *metrics != "" && !r.Restart.ReadyAt.IsZero() {
		r.Restart.New = &snapshot{}
		if err := readSnapshot(mt, r.Restart.New); err != nil {
			return err
		}
	}
	if r.Server != nil {
		if err := readSnapshot(mt, &r.Server.After); err != nil {
			stopStreams()
			wg.Wait()
			return err
		}
		b, a, d := &r.Server.Before, &r.Server.After, &r.Server.Delta
		d.Queries, d.TransactionsBegun = a.Database.Queries-b.Database.Queries, a.Database.TransactionsBegun-b.Database.TransactionsBegun
		d.EmptyAcquireCount, d.EmptyAcquireWaitNS = a.Pool.EmptyAcquireCount-b.Pool.EmptyAcquireCount, a.Pool.EmptyAcquireWaitNS-b.Pool.EmptyAcquireWaitNS
	}
	stopStreams()
	wg.Wait()
	if receiptFile != nil {
		if err := json.NewEncoder(receiptFile).Encode(runFile{Header: runHeader{1, "receipts", data.Slug, data.Channels[0], cursorNumber, final}, Streams: records}); err != nil {
			return fmt.Errorf("writing receipts: %w", err)
		}
		if err := receiptFile.Close(); err != nil {
			return fmt.Errorf("closing receipts: %w", err)
		}
	}
	if r.Reconnect != nil {
		r.Reconnect.Attempts = connectionCounts{c.connectionAttempts[connectionEstablished].Load(), c.connectionAttempts[connectionUnavailable].Load(), c.connectionAttempts[connectionRefused].Load(), c.connectionAttempts[connectionOther].Load()}
	}
	if r.Server != nil {
		time.Sleep(100 * time.Millisecond)
		if err := readSnapshot(mt, &r.Server.Closed); err != nil {
			return err
		}
	}
	r.DialFailures.TooManyOpenFiles, r.DialFailures.AddressUnavailableOrPortsExhausted, r.DialFailures.ConnectionRefused, r.DialFailures.Timeout, r.DialFailures.Other = c.dialFailures[0].Load(), c.dialFailures[1].Load(), c.dialFailures[2].Load(), c.dialFailures[3].Load(), c.dialFailures[4].Load()
	sequences := make([]uint64, 0, len(c.renders))
	for seq := range c.renders {
		sequences = append(sequences, seq)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	r.Renders.SizesBytes = make([]int, 0, len(sequences))
	for _, seq := range sequences {
		size := c.renders[seq]
		r.Renders.SizesBytes = append(r.Renders.SizesBytes, size)
		if r.Renders.Count == 0 || size < r.Renders.MinBytes {
			r.Renders.MinBytes = size
		}
		r.Renders.MaxBytes = max(r.Renders.MaxBytes, size)
		r.Renders.TotalBytes += size
		r.Renders.Count++
	}
	if r.Renders.Count > 0 {
		r.Renders.MeanBytes = float64(r.Renders.TotalBytes) / float64(r.Renders.Count)
	}
	var samples []time.Duration
	for _, p := range c.deliveries {
		if p.answered {
			samples = append(samples, p.latencies...)
			r.Duplicated += p.duplicates
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	if n := len(samples); n > 0 {
		r.P50MS, r.P95MS, r.MaxMS = float64(samples[(n*50+99)/100-1])/float64(time.Millisecond), float64(samples[(n*95+99)/100-1])/float64(time.Millisecond), float64(samples[n-1])/float64(time.Millisecond)
	}
	r.Received = uint64(len(samples))
	r.Missing = r.Expected - r.Received
	r.Established, r.Refused429, r.Refused503, r.Reset, r.Failed, r.TCPConnections = c.established.Load(), c.limited.Load(), c.shutdown.Load(), c.reset.Load(), c.failed.Load(), c.tcp.Load()
	r.PostAttemptsMade = c.postAttempts.Load()
	r.Answered200, r.PostFailed, r.HTTP2 = c.posts.Load(), c.postFailed.Load(), c.http2.Load()
	r.AchievedRate = float64(r.Sent) / r.ObservationSeconds
	r.verdict(time.Second)
	err = json.NewEncoder(out).Encode(r)
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
