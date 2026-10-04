// Command ribbitto runs the ribbitto chat server.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/setup"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/web"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func main() {
	if err := run(); err != nil {
		slog.Error("command failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// Stop on SIGTERM as well as Ctrl-C: container runtimes send SIGTERM and
	// expect the process to shut down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := os.Args[1:]
	command := "serve"
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "serve":
	case len(args) == 2 && args[0] == "migrate" && (args[1] == "up" || args[1] == "down" || args[1] == "status"):
		command = args[1]
	default:
		return fmt.Errorf("usage: ribbitto [serve | migrate up|down|status]")
	}
	if command == "serve" {
		return serve(ctx, os.Getenv("RIBBITTO_DATABASE_URL"))
	}
	db, err := platform.Open(ctx, os.Getenv("RIBBITTO_DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer func() { _ = db.Close() }()
	return platform.Migrate(ctx, db, command, os.Stdout)
}

func serve(ctx context.Context, databaseURL string) error {
	retention := realtime.DefaultRetention
	if value := os.Getenv("RIBBITTO_EVENT_RETENTION"); value != "" {
		var err error
		retention, err = time.ParseDuration(value)
		if err != nil || retention <= 0 {
			return fmt.Errorf("RIBBITTO_EVENT_RETENTION must be a positive duration")
		}
	}
	enabled, err := signupEnabled(os.Getenv("RIBBITTO_SIGNUP"))
	if err != nil {
		return err
	}
	token, err := setupToken()
	if err != nil {
		return err
	}
	trusted, err := middleware.ParseTrustedProxies(os.Getenv("RIBBITTO_TRUSTED_PROXIES"))
	if err != nil {
		return fmt.Errorf("RIBBITTO_TRUSTED_PROXIES: %w", err)
	}
	addr := os.Getenv("RIBBITTO_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	metricsAddr, queries, err := devMetricsSetup(os.Getenv("RIBBITTO_DEV_METRICS_ADDR"))
	if err != nil {
		return err
	}

	pool, err := platform.OpenPool(ctx, databaseURL, queries)
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	// Cancel cache loads before closing the pool on every exit path.
	ctx, cancelLoads := context.WithCancel(ctx)
	defer cancelLoads()
	// One hub per process: posting raises it, streams register their
	// connections with it, and the metrics read its registry.
	hub := realtime.NewHub()
	handler, sessions, err := buildHandler(ctx, pool, handlerConfig{
		setupToken: token, signupEnabled: enabled, trustedProxies: trusted,
		devAssets: os.Getenv("RIBBITTO_DEV_ASSETS"), hub: hub,
	})
	if err != nil {
		return err
	}
	// Stop cleanup before closing its pool, including on listener failure.
	stopCleanup := startSessionCleanup(ctx, sessions)
	defer stopCleanup()
	stopWatermark := startRealtimeWorker(ctx, realtime.Watermark{Hub: hub, Sequences: orgpg.NewSequences(pool)}, realtime.WatermarkInterval)
	defer stopWatermark()
	stopRetention := startRealtimeWorker(ctx, realtime.Retention{Events: realtimepg.NewCleaner(pool, orgpg.RetentionBoundaryIn), Period: retention}, time.Hour)
	defer stopRetention()

	srv := newServer(addr, handler, serverTimeouts{
		readHeader: readHeaderTimeout, read: readTimeout, idle: idleTimeout,
		write: writeTimeout,
	})
	endStreamsOnShutdown(srv, hub)

	var metrics *http.Server
	if metricsAddr != "" {
		// Listen before serving, so a taken port fails the start.
		ln, err := net.Listen("tcp", metricsAddr)
		if err != nil {
			return fmt.Errorf("RIBBITTO_DEV_METRICS_ADDR: %w", err)
		}
		metrics = newMetricsServer(metricsAddr, devMetrics{queries: queries, pool: pool, streams: hub})
		defer func() { _ = metrics.Close() }()
		slog.Warn("development metrics enabled; use only on a disposable machine", "addr", metricsAddr)
		go func() {
			if err := metrics.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("development metrics stopped", "err", err)
			}
		}()
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if metrics != nil {
		_ = metrics.Shutdown(shutdownCtx)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// The server's timeouts. Without them net/http sets no deadline once the
// headers are read, so a client could hold a connection, a goroutine and a
// file descriptor for as long as it liked: idle between requests, or with a
// request body started and never finished.
const (
	// readHeaderTimeout bounds the request line and headers. A browser sends
	// them in one or two packets, so 10 s covers a slow or lossy link many
	// times over, while a client trickling headers byte by byte is cut off.
	readHeaderTimeout = 10 * time.Second
	// readTimeout bounds reading a whole request, headers and body. Bodies
	// are capped at 64 KiB (middleware.MaxBodyBytes), which even a slow
	// mobile link sends in a few seconds.
	readTimeout = 30 * time.Second
	// idleTimeout bounds how long a keep-alive connection waits for its
	// next request; browsers reconnect cheaply after it.
	idleTimeout = 60 * time.Second
	// writeTimeout bounds every ordinary response, so a client that stops
	// reading releases its connection and handler. net/http starts it when
	// the request headers have been read, so it also covers reading the
	// body: it must exceed readTimeout, leaving 30 s or more for the handler
	// and for a page or the largest static asset on a slow link.
	writeTimeout = 60 * time.Second
)

type serverTimeouts struct {
	readHeader, read, idle, write time.Duration
}

// endStreamsOnShutdown makes srv end every open event stream when it starts
// shutting down. An open stream never goes idle, so Shutdown would wait for
// it until its deadline; ending them first lets it finish, and browsers
// reconnect to the next process.
func endStreamsOnShutdown(srv *http.Server, hub *realtime.Hub) {
	srv.RegisterOnShutdown(hub.CancelAll)
}

// newServer returns the HTTP server with its timeouts; tests pass short
// ones. Every ordinary response gets the write timeout; ribbitto does not
// rely on a reverse proxy to cut off a client that stops reading. A
// streaming endpoint, such as M3's SSE, must not simply inherit it: it sets
// its own finite deadline before each write with
// http.ResponseController.SetWriteDeadline. A future route that must read a
// long request body likewise sets its own read deadline.
func newServer(addr string, handler http.Handler, timeouts serverTimeouts) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: timeouts.readHeader,
		ReadTimeout:       timeouts.read,
		IdleTimeout:       timeouts.idle,
		WriteTimeout:      timeouts.write,
	}
}

type handlerConfig struct {
	setupToken, devAssets string
	signupEnabled         bool
	trustedProxies        []netip.Prefix
	// hub, when set, is raised by posting and serves the event stream; nil
	// leaves posting unnotified and the stream off.
	hub *realtime.Hub
	// streamWriteTimeout bounds each stream write; zero keeps the default.
	// Tests shorten it.
	streamWriteTimeout time.Duration
}

// buildHandler shares production wiring with the HTTPS acceptance test.
func buildHandler(ctx context.Context, pool *pgxpool.Pool, config handlerConfig) (http.Handler, *identity.Sessions, error) {
	// A nil hub must stay a nil canceller, not a typed nil in the interface.
	var canceller identity.SessionCanceller
	if config.hub != nil {
		// Deleting a session (sign-out, or a sign-in replacing it) ends its
		// open event streams at once.
		canceller = config.hub
	}
	sessions := identitypg.NewSessions(pool, time.Now, canceller)
	// One hasher for the whole process: its slots are the cap on concurrent
	// Argon2id work (decision 10 in docs/decisions).
	hasher, err := identity.NewHasher()
	if err != nil {
		return nil, nil, err
	}
	setupStore := postgres.NewSetupStore(pool, appendEvents)
	var setupService web.SetupService
	if config.setupToken != "" {
		setupService = setup.New(setupStore, hasher, config.setupToken)
	}

	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		return nil, nil, err
	}
	authorizer := org.NewAuthorizer(postgres.NewAuthzStore(pool))
	// A nil hub must stay a nil Notifier, not a typed nil in either interface.
	var postingNotifier message.Notifier
	var branchNotifier topic.Notifier
	var stream *web.Streaming
	if config.hub != nil {
		postingNotifier = config.hub
		branchNotifier = config.hub
		// Streams at the same cursor share each event read (#227); events never
		// change, so the TTL only bounds memory.
		kinds := postgres.EventKinds()
		for kind, router := range orgpg.EventKinds() {
			kinds[kind] = router
		}
		events := realtime.NewCachedEvents(ctx, realtimepg.NewReader(pool, orgpg.BoundsIn, kinds), config.hub, 1024, time.Minute)
		stream = &web.Streaming{Lifetime: ctx, Hub: config.hub, Events: events, Authorizer: authorizer, Sessions: sessions, WriteTimeout: config.streamWriteTimeout}
	}
	posting := message.NewWithNotifier(postgres.NewPostingStore(pool, appendEvents), postingNotifier)
	branching := topic.NewBrancher(postgres.NewBranchStore(pool, appendEvents), branchNotifier)
	handler, err := web.NewHandler(config.devAssets, catalogues, web.Services{
		Sessions:      sessions,
		SignIn:        identitypg.NewSignIn(pool, hasher, sessions),
		Setup:         setupService,
		SignUp:        signup.New(setupStore, hasher, config.signupEnabled),
		SetupSessions: sessions,
		Authz:         authorizer,
		Topics:        postgres.NewTopicStore(pool),
		Messages:      postgres.MessageReader{Pool: pool, Accounts: identitypg.AccountsIn},
		Posting:       posting,
		Branching:     branching,
		Channels:      channel.New(postgres.NewChannelStore(pool)),
		Limits:        middleware.NewAuthLimits(config.trustedProxies, time.Now),
		Stream:        stream,
	})
	if err != nil {
		return nil, nil, err
	}

	return handler, sessions, nil
}

func setupToken() (string, error) {
	token := os.Getenv("RIBBITTO_SETUP_TOKEN")
	if token != "" && utf8.RuneCountInString(token) < 32 {
		return "", fmt.Errorf("RIBBITTO_SETUP_TOKEN must be empty or at least 32 characters")
	}
	return token, nil
}

// expiredSessions is what the clean-up needs; *identity.Sessions is one.
type expiredSessions interface {
	DeleteExpired(context.Context) error
}

// startSessionCleanup deletes expired sessions at once and then hourly. The
// returned function cancels the loop, including a query in progress, and
// waits for it to finish.
func startSessionCleanup(ctx context.Context, sessions expiredSessions) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deleteExpiredSessions(ctx, sessions)
	}()
	return func() {
		cancel()
		<-done
	}
}

// startRealtimeWorker runs periodic realtime maintenance until stop, which
// waits for it, so it never outlives the pool.
func startRealtimeWorker(ctx context.Context, w interface {
	Run(context.Context, <-chan time.Time)
}, interval time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, ticker.C)
	}()
	return func() {
		cancel()
		<-done
		ticker.Stop()
	}
}

// deleteExpiredSessions runs until ctx ends. Failures are only logged:
// expired sessions are already rejected, so a missed run just leaves rows
// until the next one.
func deleteExpiredSessions(ctx context.Context, sessions expiredSessions) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := sessions.DeleteExpired(ctx); err != nil && ctx.Err() == nil {
			slog.Error("deleting expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func signupEnabled(value string) (bool, error) {
	switch value {
	case "on":
		return true, nil
	case "off", "":
		return false, nil
	default:
		return false, fmt.Errorf("RIBBITTO_SIGNUP must be on, off or empty")
	}
}

// appendEvents adapts realtime's appender to the consumer interface the
// event-writing stores declare (decision 26).
func appendEvents(tx platform.Tx) postgres.EventAppender { return realtimepg.AppenderIn(tx) }
