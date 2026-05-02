package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// PoolPinger abstracts database connectivity checking. *pgxpool.Pool satisfies
// this interface in production.
type PoolPinger interface {
	Ping(ctx context.Context) error
}

// serverConfig holds configuration needed by the server. In production this
// is populated from config.Config; in tests it can be a minimal struct.
type serverConfig struct {
	apiToken   string
	hmacSecret string
}

// NewServerConfig creates a serverConfig from the provided token and secret.
// This allows callers outside the api package to construct configuration.
func NewServerConfig(apiToken, hmacSecret string) *serverConfig {
	return &serverConfig{apiToken: apiToken, hmacSecret: hmacSecret}
}

// Server is the control plane HTTP server.
type Server struct {
	router                   chi.Router
	pinger                   PoolPinger
	config                   *serverConfig
	startTime                time.Time
	logger                   *slog.Logger
	nodeStore                NodeStore
	lifecycle                SandboxLifecycle
	sandboxReader            SandboxReader
	agentStore               AgentStore
	agentLifecycle           AgentLifecycle
	filePushStore            FilePushStore
	metricsStore             MetricsStore
	routeFetcher             RouteListFetcher
	eventStore               EventStore
	logProxyStore            LogProxyStore
	logHTTPClient            httpDoer
	heartbeatIntervalSeconds int
	reconciler               Reconciler // Phase 30 — may be nil; handler tolerates nil and skips reconcile
}

// NewServer creates a new Server with chi router, middleware, and route groups.
// cfg may be nil for tests that only need public routes.
// logger may be nil (a default logger will be used).
// nodeStore may be nil if node handlers are not needed.
// lifecycle and sandboxReader may be nil if sandbox handlers are not needed.
// heartbeatIntervalSec defaults to 15 if 0.
func NewServer(cfg *serverConfig, pinger PoolPinger, logger *slog.Logger, nodeStore NodeStore, lc SandboxLifecycle, sr SandboxReader, heartbeatIntervalSec int) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if heartbeatIntervalSec == 0 {
		heartbeatIntervalSec = 15
	}

	s := &Server{
		pinger:                   pinger,
		config:                   cfg,
		startTime:                time.Now(),
		logger:                   logger,
		nodeStore:                nodeStore,
		lifecycle:                lc,
		sandboxReader:            sr,
		heartbeatIntervalSeconds: heartbeatIntervalSec,
	}

	s.RegisterRoutes()
	return s
}

// SetAgentDeps injects agent endpoint dependencies after construction.
// This avoids widening the NewServer signature for optional deps.
func (s *Server) SetAgentDeps(as AgentStore, al AgentLifecycle) {
	s.agentStore = as
	s.agentLifecycle = al
}

// SetFilePushStore injects the file push store dependency after construction.
func (s *Server) SetFilePushStore(fps FilePushStore) {
	s.filePushStore = fps
}

// SetMetricsStore injects the metrics store dependency after construction.
func (s *Server) SetMetricsStore(ms MetricsStore) {
	s.metricsStore = ms
}

// SetRouteFetcher injects the route list fetcher dependency after construction.
func (s *Server) SetRouteFetcher(rf RouteListFetcher) {
	s.routeFetcher = rf
}

// SetEventStore injects the event store dependency after construction.
func (s *Server) SetEventStore(es EventStore) {
	s.eventStore = es
}

// SetReconciler injects the heartbeat Reconciler after construction.
// Phase 30 — the concrete impl is internal/scheduler.HeartbeatReconciler (T7).
// When wired, handleHeartbeat calls Reconcile on rich heartbeats (those carrying
// req.Containers). When nil, the handler still acks heartbeats normally and
// just skips the reconcile branch — backwards compat for legacy agent rollout.
func (s *Server) SetReconciler(r Reconciler) {
	s.reconciler = r
}

// SetLogProxyStore injects the log proxy store dependency after construction.
// httpClient defaults to http.DefaultClient with a 60-second timeout if nil (T-06-02).
func (s *Server) SetLogProxyStore(lps LogProxyStore, httpClient httpDoer) {
	s.logProxyStore = lps
	if httpClient != nil {
		s.logHTTPClient = httpClient
	} else {
		s.logHTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
}

// httpDoer abstracts HTTP client for testability (used by log proxy).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ServeHTTP delegates to the chi router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Router returns the underlying chi router for testing or extension.
func (s *Server) Router() chi.Router {
	return s.router
}
