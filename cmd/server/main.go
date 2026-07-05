package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zaentrum/katalog-api/internal/auth"
	"github.com/zaentrum/katalog-api/internal/config"
	katalogapihttp "github.com/zaentrum/katalog-api/internal/http"
	"github.com/zaentrum/katalog-api/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	slog.Info("starting katalog-api",
		"addr", cfg.Addr,
		"oidc_issuer", cfg.OIDCIssuer,
		"oidc_audience", cfg.OIDCAudience,
		"pg_set", cfg.PgURL != "",
	)

	ctx := context.Background()

	st, err := store.New(ctx, cfg.PgURL)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// NewVerifier is non-fatal on an unreachable issuer: it returns a live
	// verifier that fails closed and retries OIDC discovery in the background
	// (so a cold Keycloak at boot self-heals instead of permanently disabling
	// auth). An error here means a real config problem (empty issuer/audiences)
	// — still let the service come up so /healthz can answer.
	verifier, err := auth.NewVerifier(ctx, cfg.OIDCIssuer, cfg.OIDCAudience)
	if err != nil {
		slog.Warn("oidc verifier misconfigured; auth middleware will reject all requests", "err", err)
	}

	router, err := katalogapihttp.NewRouter(cfg, st, verifier)
	if err != nil {
		slog.Error("router init failed", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("http listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	<-stopCtx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
