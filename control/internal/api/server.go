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
	apiToken string
}

// Server is the control plane HTTP server.
type Server struct {
	router    chi.Router
	pinger    PoolPinger
	config    *serverConfig
	startTime time.Time
	logger    *slog.Logger
}

// NewServer creates a new Server with chi router, middleware, and route groups.
// cfg may be nil for tests that only need public routes.
// logger may be nil (a default logger will be used).
func NewServer(cfg *serverConfig, pinger PoolPinger, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		pinger:    pinger,
		config:    cfg,
		startTime: time.Now(),
		logger:    logger,
	}

	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Public routes (no auth required)
	r.Get("/v1/healthz", s.handleHealthz)

	// Authenticated routes
	if cfg != nil && cfg.apiToken != "" {
		r.Group(func(r chi.Router) {
			r.Use(BearerAuth(cfg.apiToken))
			r.Route("/v1", func(r chi.Router) {
				// Placeholder: handler plans will mount their routes here.
				// For now, a catchall returns 404 to confirm auth works.
				r.Get("/sandboxes", func(w http.ResponseWriter, r *http.Request) {
					NotFound(w, "not implemented")
				})
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
