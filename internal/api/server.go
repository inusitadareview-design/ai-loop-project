// Package api implements the HTTP backend that serves the React
// dashboard and exposes a REST API for pipeline management
// (ARCHITECTURE.md 4.16, invariant I6).
//
// Router: chi (lightweight, idiomatic, zero magic).
// CORS: enabled for local dashboard development.
// Bind: 127.0.0.1 by default (guardrail G2.2: no accidental exposure).
//
// Guardrails enforced:
//   - G2.1 — secrets never appear in responses (pipeline YAML has no auth)
//   - G2.2 — handlers only read config/ and data/ paths
//   - G4.3 — pipeline execution uses scheduler.RunNow (lock per pipeline)
//   - G5.1 — every error wrapped with fmt.Errorf
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
}

// NewServer builds a Server wired to all runtime dependencies.
// It constructs the chi router and registers all routes.
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
		// Allow dashboard origin; permissive for local dev.
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

	// Public routes (no auth in increment 1).
	r.Get("/health", h.health)
	r.Get("/version", h.versionHandler)

	// REST API.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/pipelines", h.listPipelines)
		r.Get("/pipelines/{name}", h.getPipeline)
		r.Post("/pipelines/{name}/run", h.runPipeline)
		r.Delete("/pipelines/{name}/failures", h.resetPipelineFailures) // <-- ADICIONAR ESTA LINHA
		r.Get("/pipelines/{name}/memory", h.getPipelineMemory)
	})

	return &Server{
		cfg:       cfg,
		logger:    logger,
		scheduler: sched,
		memory:    mem,
		engine:    eng,
		version:   version,
		router:    r,
	}, nil
}

// Router returns the underlying chi.Router. Used by tests to exercise
// routes via httptest without binding to a port.
func (s *Server) Router() chi.Router { return s.router }

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
