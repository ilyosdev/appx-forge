package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
	heartbeatIntervalSeconds int
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

	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Public routes (no auth required)
	r.Get("/v1/healthz", s.handleHealthz)
	r.Post("/v1/nodes/register", s.handleRegisterNode)

	// Authenticated routes
	if cfg != nil && cfg.apiToken != "" {
		r.Group(func(r chi.Router) {
			r.Use(BearerAuth(cfg.apiToken))
			r.Route("/v1", func(r chi.Router) {
				r.Post("/nodes/{id}/heartbeat", s.handleHeartbeat)

				// Sandbox CRUD
				r.Post("/sandboxes", s.handleCreateSandbox)
				r.Get("/sandboxes", s.handleListSandboxes)
				r.Get("/sandboxes/{id}", s.handleGetSandbox)
				r.Delete("/sandboxes/{id}", s.handleDestroySandbox)
				r.Post("/sandboxes/{id}/restart", s.handleRestartSandbox)
				r.Post("/sandboxes/{id}/files", s.handleFilePush)

				// Agent endpoints
				r.Get("/agents/{id}/commands", s.handlePollCommands)
				r.Post("/agents/{id}/commands/{cmd_id}/ack", s.handleAckCommand)
				r.Post("/agents/{id}/events", s.handleReportEvent)
			})
		})
	}

	s.router = r
	return s
}

// ServeHTTP delegates to the chi router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Router returns the underlying chi router for testing or extension.
func (s *Server) Router() chi.Router {
	return s.router
}
