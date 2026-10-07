// Command loadgen exercises a disposable loopback server through public HTTP endpoints.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
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

// maxStreams covers #216's idle steps (10k, 20k …) with room to spare and
// keeps the run finite.
const maxStreams = 100_000

type counts struct {
	established, limited, shutdown, reset, failed, posts, postFailed, tcp, received atomic.Uint64
	http2                                                                           atomic.Bool
	mu                                                                              sync.Mutex
	deliveries                                                                      map[string]*post
	frozen                                                                          bool
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
	Cursor, StreamsAttempted, Established, Refused429, Refused503, Reset, Failed, TCPConnections                        uint64
	Scheduled, Sent, Answered200, PostFailed, Missed, Expected, Received, Missing, Duplicated                           uint64
	Rate, DialConcurrency                                                                                               int
	DurationSeconds, SetupLimitSeconds, DrainLimitSeconds, SetupSeconds, ObservationSeconds, DrainSeconds, AchievedRate float64
	P50MS, P95MS, MaxMS                                                                                                 float64
	CursorOverride, CursorValid, HTTP2, Slow, DeliveryMissing, RequestFailed                                            bool
	Verdict                                                                                                             string
}

func (c *counts) receive(data string, seen map[*post]bool) {
	arrival := time.Now()
	markers := c.markers.FindAllString(data, -1)
	sort.Strings(markers)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen || (!c.deadline.IsZero() && arrival.After(c.deadline)) {
		return
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
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: loopback}
	t := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err == nil {
			c.tcp.Add(1)
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
// Payloads are examined for this run's markers, never logged; no reconnects.
func readReset(body io.Reader, receive func(string)) bool {
	s := bufio.NewScanner(body)
	s.Buffer(make([]byte, 4096), 1<<20)
	event, data := "", false
	var payload strings.Builder
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if event == "reset" && data {
				return true
			}
			if data && receive != nil {
				receive(payload.String())
			}
			payload.Reset()
			event, data = "", false
		}
		field, value, _ := strings.Cut(line, ":")
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

func stream(ctx context.Context, client *http.Client, endpoint, token, cursor string, c *counts, setup context.Context, opened func()) {
	notify := sync.OnceFunc(opened)
	defer notify()
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(setup, cancel)
	r, err := request(dialCtx, client, http.MethodGet, endpoint, token, cursor)
	stop()
	if err != nil {
		// The harness closing the run is not a failed stream.
		if ctx.Err() == nil {
			c.failed.Add(1)
		}
		return
	}
	defer func() { _ = r.Body.Close() }()
	if r.ProtoMajor == 2 {
		c.http2.Store(true)
	}
	switch r.StatusCode {
	case http.StatusTooManyRequests:
		c.limited.Add(1)
	case http.StatusServiceUnavailable:
		c.shutdown.Add(1)
	case http.StatusOK:
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
			c.failed.Add(1)
			return
		}
		c.established.Add(1)
		notify()
		seen := make(map[*post]bool)
		if readReset(r.Body, func(data string) { c.receive(data, seen) }) {
			c.reset.Add(1)
		} else if ctx.Err() == nil {
			c.failed.Add(1)
		}
	default:
		c.failed.Add(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("loadgen (development only)", flag.ContinueOnError)
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
	if flags.NArg() != 0 || *duration <= 0 || *duration > 10*time.Minute || *streams < 1 || *streams > maxStreams || *rate < 0 || *rate > 100 || *drain <= 0 || *drain > 5*time.Minute || *setup <= 0 || *setup > 5*time.Minute || *dials < 1 || *dials > maxStreams {
		return fmt.Errorf("duration, streams or rate outside finite limits")
	}
	c := &counts{}
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
	client := &http.Client{Transport: t}
	streamCtx, stopStreams := context.WithCancel(context.Background())
	defer stopStreams()
	endpoint := t.origin.Scheme + "://" + t.origin.Host + "/organizations/" + url.PathEscape(data.Slug) + "/channels/" + url.PathEscape(data.Channels[0])
	tokenAt := func(i int) string {
		a := data.Accounts[i%len(data.Accounts)]
		return a.Tokens[(i/len(data.Accounts))%len(a.Tokens)]
	}
	explicitCursor := false
	flags.Visit(func(f *flag.Flag) { explicitCursor = explicitCursor || f.Name == "cursor" })
	if !explicitCursor {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		page, err := request(ctx, client, http.MethodGet, endpoint, tokenAt(0), "")
		if err != nil {
			return fmt.Errorf("cannot read channel cursor")
		}
		html, err := io.ReadAll(io.LimitReader(page.Body, 16<<20))
		_ = page.Body.Close()
		if page.ProtoMajor == 2 {
			c.http2.Store(true)
		}
		match := regexp.MustCompile(`sse-connect="[^"\n]*/events\?after=([0-9]+)"`).FindSubmatch(html)
		if err != nil || page.StatusCode != http.StatusOK || len(match) != 2 {
			return fmt.Errorf("invalid channel cursor")
		}
		*cursor = string(match[1])
	}
	prefix := "loadgen" + rand.Text()
	c.markers, c.deliveries = regexp.MustCompile(prefix+`[0-9]+Z`), make(map[string]*post)
	cursorNumber, cursorErr := strconv.ParseUint(*cursor, 10, 64)
	r := result{Cursor: cursorNumber, CursorValid: cursorErr == nil, CursorOverride: explicitCursor, StreamsAttempted: uint64(*streams), Rate: *rate, DialConcurrency: *dials, DurationSeconds: duration.Seconds(), SetupLimitSeconds: setup.Seconds(), DrainLimitSeconds: drain.Seconds()}
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
				stream(streamCtx, client, endpoint+"/events", tokenAt(i), *cursor, c, setupCtx, func() { <-slots; ready.Done() })
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
	var posts sync.WaitGroup
	inflight := make(chan struct{}, *rate)
	if *rate > 0 {
		r.Scheduled = (uint64(*duration)*uint64(*rate) + uint64(time.Second) - 1) / uint64(time.Second)
	}
	for i := uint64(0); i < r.Scheduled; i++ {
		time.Sleep(time.Until(start.Add(time.Duration(i) * time.Second / time.Duration(*rate))))
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
			ctx, cancel := context.WithTimeout(streamCtx, 10*time.Second)
			defer cancel()
			// Post-to-receipt latency starts here, not when the slot was
			// scheduled, so the client's own goroutine scheduling is excluded.
			c.mu.Lock()
			p.sent = time.Now()
			c.mu.Unlock()
			response, err := request(ctx, client, http.MethodPost, endpoint, tokenAt(int(i)), marker)
			if err == nil {
				if response.ProtoMajor == 2 {
					c.http2.Store(true)
				}
				_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
				_ = response.Body.Close()
			}
			if err == nil && response.StatusCode == http.StatusOK {
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
	time.Sleep(time.Until(start.Add(*duration)))
	r.ObservationSeconds = time.Since(start).Seconds()
	posts.Wait() // Finalize the answered-200 set before deciding the drain is complete.
	start = time.Now()
	r.Expected = c.established.Load() * c.posts.Load()
	c.mu.Lock()
	c.deadline = start.Add(*drain)
	c.mu.Unlock()
	for {
		if c.received.Load() == r.Expected || time.Since(start) >= *drain {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.mu.Lock()
	c.frozen = true
	c.mu.Unlock()
	r.DrainSeconds = time.Since(start).Seconds()
	stopStreams()
	wg.Wait()
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
