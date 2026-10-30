// server.go wires the chi router, middlewares, and WebSocket hub into
// an HTTP server that serves the React dashboard and REST API
// (ARCHITECTURE.md 4.16, invariant I6).
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog/log"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/scheduler"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// Server is the HTTP API server. Build one per process via NewServer.
type Server struct {
	cfg       *config.Config
	logger    *observability.Logger
	scheduler *scheduler.Scheduler
	memory    *memory.Store
	engine    *engine.Engine
	version   string
	router    chi.Router
	httpSrv   *http.Server
	hub       *WSHub
}

// NewServer builds a Server wired to all runtime dependencies.
// It constructs the chi router, registers all routes (including the
// WebSocket endpoint at /api/v1/ws), and starts the hub's event loop.
func NewServer(
	cfg *config.Config,
	logger *observability.Logger,
	sched *scheduler.Scheduler,
	mem *memory.Store,
	eng *engine.Engine,
	version string,
) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("api: config must not be nil")
	}
	if logger == nil {
		return nil, errors.New("api: logger must not be nil")
	}
	if sched == nil {
		return nil, errors.New("api: scheduler must not be nil")
	}
	if mem == nil {
		return nil, errors.New("api: memory store must not be nil")
	}
	if eng == nil {
		return nil, errors.New("api: engine must not be nil")
	}

	// Initialize WebSocket hub and start its event loop in the
	// background. The hub will accept connections as soon as the
	// HTTP server starts listening.
	hub := NewWSHub()
	go hub.Start(context.Background())

	h := &handlers{
		cfg:       cfg,
		logger:    logger,
		scheduler: sched,
		memory:    mem,
		engine:    eng,
		version:   version,
	}

	r := chi.NewRouter()

	// Global middlewares.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{
			"http://localhost:*",
			"http://127.0.0.1:*",
			"http://localhost",
			"http://127.0.0.1",
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Public routes (no auth in current increment).
	r.Get("/health", h.health)
	r.Get("/version", h.versionHandler)

	// REST API + WebSocket endpoint.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/pipelines", h.listPipelines)
		r.Get("/pipelines/{name}", h.getPipeline)
		r.Post("/pipelines/{name}/run", h.runPipeline)
		r.Delete("/pipelines/{name}/failures", h.resetPipelineFailures)
		r.Get("/pipelines/{name}/memory", h.getPipelineMemory)
		r.Get("/ws", handleWS(hub))
		// Draft management endpoints (G3.2: human approval)
		r.Get("/drafts", h.listDrafts)
		r.Get("/drafts/{id}", h.getDraft)
		r.Post("/drafts/{id}/approve", h.approveDraft)
		r.Post("/drafts/{id}/reject", h.rejectDraft)
	})

	return &Server{
		cfg:       cfg,
		logger:    logger,
		scheduler: sched,
		memory:    mem,
		engine:    eng,
		version:   version,
		router:    r,
		httpSrv:   nil, // initialized in ListenAndServe
		hub:       hub,
	}, nil
}

// Router returns the underlying chi.Router. Used by tests to exercise
// routes via httptest without binding to a port.
func (s *Server) Router() chi.Router { return s.router }

// Hub returns the WebSocket hub for external event emission.
// Used by the scheduler (future increment) to broadcast pipeline
// lifecycle events to all connected dashboard clients.
func (s *Server) Hub() *WSHub { return s.hub }

// ListenAndServe binds to cfg.Server.Host:cfg.Server.Port and serves
// HTTP until ctx is canceled. On cancel, performs graceful shutdown
// with a 10-second deadline. Returns nil on graceful shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Server.Host, fmt.Sprintf("%d", s.cfg.Server.Port))

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", addr).Msg("api: HTTP server listening")
		if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		log.Info().Msg("api: shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("api: shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("api: serve: %w", err)
		}
		return nil
	}
}
