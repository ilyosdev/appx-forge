package api

import (
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	apimw "github.com/appx/forge/control/internal/api/middleware"
)

// RegisterRoutes configures the chi router with all route groups:
//   - Public group (no auth): GET /v1/healthz, POST /v1/nodes/register
//   - Authenticated group (BearerAuth middleware): all other /v1/* routes
func (s *Server) RegisterRoutes() {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))

	// Public routes (no auth required)
	r.Get("/v1/healthz", s.handleHealthz)
	r.Get("/v1/metrics", s.handleMetrics)
	r.Post("/v1/nodes/register", s.handleRegisterNode)

	// Authenticated routes
	if s.config != nil && s.config.apiToken != "" {
		r.Group(func(r chi.Router) {
			r.Use(BearerAuth(s.config.apiToken))
			r.Route("/v1", func(r chi.Router) {
				// Node management
				r.Get("/nodes", s.handleListNodes)
				r.Post("/nodes/{id}/heartbeat", s.handleHeartbeat)
				r.Post("/nodes/{id}/drain", s.handleDrainNode)
				r.Delete("/nodes/{id}", s.handleRemoveNode)

				// Sandbox CRUD
				r.Post("/sandboxes", s.handleCreateSandbox)
				r.Get("/sandboxes", s.handleListSandboxes)
				r.Get("/sandboxes/{id}", s.handleGetSandbox)
				r.Delete("/sandboxes/{id}", s.handleDestroySandbox)
				r.Post("/sandboxes/{id}/restart", s.handleRestartSandbox)
				r.Post("/sandboxes/{id}/wake", s.handleWakeSandbox)
				r.Post("/sandboxes/{id}/files", s.handleFilePush)

				// Exec endpoints — optionally accept a scoped JWT in the
				// X-Exec-Token header (in addition to the global Bearer).
				// When the secret is configured the JWT middleware enforces
				// sandbox_id claim binding; when unset it pass-throughs
				// and only the outer Bearer middleware authorizes.
				r.Group(func(r chi.Router) {
					if s.config != nil {
						r.Use(apimw.ExecJWT(s.config.execJWTSecret))
					}
					r.Post("/sandboxes/{id}/exec", s.handleExecSandbox)
					r.Get("/sandboxes/{id}/exec/{cmd_id}", s.handleGetExecResult)
				})

				r.Get("/sandboxes/{id}/logs", s.handleGetLogs)

				// Routes and events
				r.Get("/routes", s.handleListRoutes)
				r.Get("/events", s.handleListEvents)

				// Agent endpoints
				r.Get("/agents/{id}/commands", s.handlePollCommands)
				r.Post("/agents/{id}/commands/{cmd_id}/ack", s.handleAckCommand)
				r.Post("/agents/{id}/events", s.handleReportEvent)
			})
		})
	}

	s.router = r
}
