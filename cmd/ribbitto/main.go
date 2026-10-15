// Command ribbitto runs the ribbitto chat server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/presence"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
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
	return serveWithLifecycle(ctx, databaseURL, newServiceLifecycle)
}

func serveWithLifecycle(ctx context.Context, databaseURL string, own func(context.Context, *realtime.Hub, func()) *serviceLifecycle) (result error) {
	streamCap, err := maxStreams()
	if err != nil {
		return err
	}
	authorizationCapacity, err := authorizationCacheCapacity(streamCap)
	if err != nil {
		return err
	}
	maxChannels, err := maxChannelsConfiguration(os.Getenv("RIBBITTO_MAX_CHANNELS"))
	if err != nil {
		return err
	}
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
	hub := realtime.NewHubWithMaxStreams(streamCap)
	computationLimit := max(1, int(pool.Config().MaxConns)/2)
	limit := realtime.NewRecountLimit(computationLimit)
	lifetime := own(ctx, hub, pool.Close)
	defer func() { result = errors.Join(result, lifetime.close()) }()
	slog.Info("recount capacity configuration", "pool_max_conns", pool.Config().MaxConns,
		"computation_limit", computationLimit, "registration_limit", streamCap)
	// One hub per process: posting raises it, streams register their
	// connections with it, and the metrics read its registry.
	handler, sessions, err := buildHandler(lifetime.ctx, pool, handlerConfig{
		maxChannels: maxChannels, setupToken: token, signupEnabled: enabled, trustedProxies: trusted,
		devAssets: os.Getenv("RIBBITTO_DEV_ASSETS"), hub: hub,
		recountLimit:          limit,
		authorizationCapacity: authorizationCapacity,
	})
	if err != nil {
		return err
	}
	// Stop cleanup before closing its pool, including on listener failure.
	lifetime.workers = append(lifetime.workers, startSessionCleanup(lifetime.ctx, sessions))
	lifetime.workers = append(lifetime.workers, startRealtimeWorker(lifetime.ctx, realtime.Watermark{Hub: hub, Sequences: orgpg.NewSequences(pool)}, realtime.WatermarkInterval))
	lifetime.workers = append(lifetime.workers, startRealtimeWorker(lifetime.ctx, realtime.Retention{Events: realtimepg.NewCleaner(pool, orgpg.RetentionBoundaryIn), Period: retention}, time.Hour))

	srv := newServer(addr, handler, serverTimeouts{
		readHeader: readHeaderTimeout, read: readTimeout, idle: idleTimeout,
		write: writeTimeout,
	})

	if metricsAddr != "" {
		// Listen before serving, so a taken port fails the start.
		ln, err := net.Listen("tcp", metricsAddr)
		if err != nil {
			return fmt.Errorf("RIBBITTO_DEV_METRICS_ADDR: %w", err)
		}
		metrics := newMetricsServer(metricsAddr, devMetrics{queries: queries, pool: pool, streams: hub})
		lifetime.startServer(metrics, func() error { return metrics.Serve(ln) })
		slog.Warn("development metrics enabled; use only on a disposable machine", "addr", metricsAddr)
	}

	slog.Info("listening", "addr", addr)
	lifetime.startServer(srv, srv.ListenAndServe)

	select {
	case <-lifetime.serverDone:
	case <-ctx.Done():
	}
	return nil
}

// authorizationCacheCapacity sizes the default for live stream keys while
// preserving explicit choices: several streams can share one cached allow.
func authorizationCacheCapacity(streamCap int) (int, error) {
	value, set := os.LookupEnv("RIBBITTO_AUTHORIZATION_CACHE_CAPACITY")
	if !set {
		return max(org.DefaultAuthorizationCapacity, streamCap), nil
	}
	capacity, err := strconv.Atoi(value)
	if err != nil || capacity <= 0 {
		return 0, fmt.Errorf("RIBBITTO_AUTHORIZATION_CACHE_CAPACITY must be a positive integer")
	}
	if capacity < streamCap {
		slog.Warn("authorization cache capacity is below the stream limit; distinct stream keys may cause cache churn",
			"RIBBITTO_AUTHORIZATION_CACHE_CAPACITY", capacity, "RIBBITTO_MAX_STREAMS", streamCap)
	}
	return capacity, nil
}

// maxStreams distinguishes an unset variable from an explicitly empty value.
func maxStreams() (int, error) {
	value, set := os.LookupEnv("RIBBITTO_MAX_STREAMS")
	if !set {
		return realtime.DefaultMaxStreams, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("RIBBITTO_MAX_STREAMS must be a positive integer")
	}
	return limit, nil
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

// newServer returns the HTTP server with its timeouts; tests pass short
// ones. Every ordinary response gets the write timeout; ribbitto does not
// rely on a reverse proxy to cut off a client that stops reading. A
// streaming endpoint, such as M3's SSE, must not simply inherit it: it sets
// its own finite deadline before each write with
// http.ResponseController.SetWriteDeadline. A future route that must read a
// long request body likewise sets its own read deadline.
func newServer(addr string, handler http.Handler, timeouts serverTimeouts) *http.Server {
	// Always pass an explicit handler: pprof's init populates DefaultServeMux.
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
	recountLimit          *realtime.RecountLimit
	maxChannels           int32
	authorizationCapacity int
	setupToken, devAssets string
	signupEnabled         bool
	trustedProxies        []netip.Prefix
	// hub, when set, is raised by posting and serves the event stream; nil
	// leaves posting unnotified and the stream off.
	hub *realtime.Hub
	// streamWriteTimeout bounds each stream write; zero keeps the default.
	// Tests shorten it.
	streamWriteTimeout time.Duration
	readLimits         *middleware.MemberRateLimiter // nil uses the production budget
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
	if config.readLimits == nil {
		config.readLimits = middleware.NewReadRateLimiter(time.Now)
	}
	sessions := identitypg.NewSessions(pool, time.Now, canceller)
	// One hasher for the whole process: its slots are the cap on concurrent
	// Argon2id work (decision 10 in docs/decisions).
	hasher, err := identity.NewHasher()
	if err != nil {
		return nil, nil, err
	}
	var setupService web.SetupService
	if config.setupToken != "" {
		setupService = orgpg.NewSetup(pool, hasher, config.setupToken,
			accountCreator,
			memberEvents,
			defaultChannelCreator)
	}

	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		return nil, nil, err
	}
	authorizer := orgpg.NewCachedAuthorizer(ctx, pool, config.authorizationCapacity)
	// A nil hub must stay a nil Notifier, not a typed nil in the interface.
	var postingNotifier conversation.Notifier
	var readPublisher unread.ReadPublisher
	var sidebarResync web.SidebarResync
	var stream *web.Streaming
	var typingState *typing.State
	if config.hub != nil {
		postingNotifier = config.hub
		changes := &unread.Changes{}
		readPublisher = changes
		sidebarResync = changes
		kinds, err := realtime.MergeKinds(conversationpg.EventKinds(), orgpg.EventKinds())
		if err != nil {
			return nil, nil, fmt.Errorf("merging event kinds: %w", err)
		}
		// Streams at the same cursor share each event read (#227); events never
		// change, so the TTL only bounds memory.
		events := realtime.NewCachedEvents(ctx, realtimepg.NewReader(pool, orgpg.BoundsIn, kinds), config.hub, 1024, time.Minute)
		state := presence.New(config.hub)
		typingState = typing.New(config.hub)
		stream = &web.Streaming{Lifetime: ctx, Hub: config.hub, Events: events, Authorizer: authorizer, Sessions: sessions, WriteTimeout: config.streamWriteTimeout,
			RecountLimit: config.recountLimit,
			Sidebar:      sidebarSource(conversationpg.NewReader(pool, nil, nil, eventCursor, pageCountsIn), changes),
			Presence:     state, StreamOpened: func(organizationID, memberID, channelID kernel.ID) func() {
				closePresence, closeTyping := state.Open(organizationID, memberID), typingState.Open(organizationID, memberID, channelID)
				return func() { closePresence(); closeTyping() }
			},
			Owners: map[realtime.Interest]realtime.EphemeralOwner{realtime.InterestPresence: web.NewPresenceOwner(ctx, state), realtime.InterestTyping: web.NewTypingOwner(typingState)}}
	}
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, postReads, postingNotifier)
	branching := conversationpg.NewBrancher(pool, postingSequence, postingEvents, branchReads, postingNotifier)
	handler, err := web.NewHandler(config.devAssets, catalogues, web.Services{
		Sessions: sessions,
		SignIn:   identitypg.NewSignIn(pool, hasher, sessions),
		Setup:    setupService,
		SignUp: orgpg.NewSignUp(pool, hasher, config.signupEnabled,
			accountCreator,
			memberEvents),
		SetupSessions: sessions,
		Authz:         authorizer,
		Topics:        conversationpg.NewTopics(pool),
		Messages:      conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, pageCountsIn),
		Posting:       posting,
		SidebarResync: sidebarResync,
		Reading:       unreadpg.NewReading(pool, newFeedWriter(), newTopicWriter(), readPublisher),
		Branching:     branching,
		Channels:      conversationpg.NewChannels(pool, config.maxChannels),
		Limits:        middleware.NewAuthLimits(config.trustedProxies, time.Now),
		Stream:        stream,
		Typing:        typingState,
		SignalLimits:  middleware.NewMemberRateLimiter(time.Now),
		ReadLimits:    config.readLimits,
		CreateLimits:  middleware.NewChannelCreationRateLimiter(time.Now),
		ResyncLimits:  middleware.NewSidebarResyncRateLimiter(time.Now),
		BranchLimits:  middleware.NewBranchRateLimiter(time.Now),
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

// accountCreator binds identity's account creator to setup's and sign-up's transaction.
func accountCreator(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }

// memberEvents binds realtime's appender to setup's and sign-up's transaction.
func memberEvents(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) }

// defaultChannelCreator binds conversation's default-channel creator to setup's transaction.
func defaultChannelCreator(tx platform.Tx) org.DefaultChannelCreator {
	return conversationpg.DefaultChannelCreatorIn(tx)
}

// postingEvents binds realtime's appender to conversation's posting and branching transaction.
func postingEvents(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }

// postingSequence binds org's sequence to conversation's posting and branching transaction.
func postingSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }

// lookupMembers adapts org's directory to the reader's consumer interface.
func lookupMembers(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) }

// lookupAccounts adapts identity's directory to the reader's consumer interface.
func lookupAccounts(s platform.Snapshot) conversation.AccountDirectory {
	return identitypg.AccountsIn(s)
}

// eventCursor adapts org's committed event_seq to the reader's consumer
// interface.
func eventCursor(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) }

func maxChannelsConfiguration(value string) (int32, error) {
	if value == "" {
		return conversation.DefaultMaxChannels, nil
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("RIBBITTO_MAX_CHANNELS must be a positive decimal integer within int32")
		}
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("RIBBITTO_MAX_CHANNELS must be a positive decimal integer within int32")
	}
	return int32(n), nil
}
