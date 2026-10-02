// Command loadgen exercises a disposable loopback server through public HTTP endpoints.
package main

import (
	"bufio"
	"context"
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

type counts struct{ established, limited, shutdown, reset, failed, posts, postFailed, tcp atomic.Uint64 }

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
		body = strings.NewReader("body=load+test")
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	r.AddCookie(&http.Cookie{Name: "__Host-session", Value: token})
	if method == http.MethodGet {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Last-Event-ID", cursor)
	} else {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", r.URL.Scheme+"://"+r.URL.Host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("HX-Request", "true")
	}
	return client.Do(r)
}

// A reset is dispatched only at a complete SSE event with a data field.
// Payloads are discarded, never logged; no reconnect policy belongs here.
func readReset(body io.Reader) bool {
	s := bufio.NewScanner(body)
	s.Buffer(make([]byte, 4096), 1<<20)
	event, data := "", false
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if event == "reset" && data {
				return true
			}
			event, data = "", false
		}
		field, value, _ := strings.Cut(line, ":")
		if field == "event" {
			event = strings.TrimPrefix(value, " ")
		}
		data = data || field == "data"
	}
	return false
}

func stream(ctx context.Context, client *http.Client, endpoint, token, cursor string, c *counts) {
	r, err := request(ctx, client, http.MethodGet, endpoint, token, cursor)
	if err != nil {
		// The harness closing the run is not a failed stream.
		if ctx.Err() == nil {
			c.failed.Add(1)
		}
		return
	}
	defer func() { _ = r.Body.Close() }()
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
		if readReset(r.Body) {
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
	cursor := flags.String("cursor", "0", "Last-Event-ID; empty omits it (server returns 400)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("invalid flags")
	}
	if flags.NArg() != 0 || *duration <= 0 || *duration > 10*time.Minute || *streams < 1 || *streams > maxStreams || *rate < 0 || *rate > 100 {
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
	// The run ends in order: posting stops at the deadline, the last POST
	// finishes on its own timeout, and only then are the streams closed. So
	// the harness's own end never shows up as a failed post or stream.
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	streamCtx, stopStreams := context.WithCancel(context.Background())
	defer stopStreams()
	endpoint := t.origin.Scheme + "://" + t.origin.Host + "/organizations/" + url.PathEscape(data.Slug) + "/channels/" + url.PathEscape(data.Channels[0])
	tokenAt := func(i int) string {
		a := data.Accounts[i%len(data.Accounts)]
		return a.Tokens[(i/len(data.Accounts))%len(a.Tokens)]
	}
	var wg sync.WaitGroup
	for i := range *streams {
		wg.Go(func() { stream(streamCtx, client, endpoint+"/events", tokenAt(i), *cursor, c) })
	}
	if *rate > 0 {
		ticker := time.NewTicker(time.Second / time.Duration(*rate))
		defer ticker.Stop()
		for i := 0; ctx.Err() == nil; i++ {
			select {
			case <-ctx.Done():
			case <-ticker.C:
				if ctx.Err() != nil {
					continue // a tick that raced the deadline sends nothing
				}
				postCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				r, err := request(postCtx, client, http.MethodPost, endpoint, tokenAt(i), "")
				if err == nil {
					_, err = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
					_ = r.Body.Close()
				}
				if err == nil && r.StatusCode == http.StatusOK {
					c.posts.Add(1)
				} else {
					c.postFailed.Add(1)
				}
				stop()
			}
		}
	}
	<-ctx.Done()
	stopStreams()
	wg.Wait()
	_, err = fmt.Fprintf(out, "streams=%d established=%d refused_429=%d refused_503=%d reset=%d failed=%d tcp_connections=%d posts=%d post_failed=%d\n", *streams, c.established.Load(), c.limited.Load(), c.shutdown.Load(), c.reset.Load(), c.failed.Load(), c.tcp.Load(), c.posts.Load(), c.postFailed.Load())
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
