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

	verifier, err := auth.NewVerifier(ctx, cfg.OIDCIssuer, cfg.OIDCAudience)
	if err != nil {
		// Don't fatal in scaffold — let the service come up even if SSO is
		// unreachable so /healthz can answer. Real wiring will go strict
		// once endpoints land.
		slog.Warn("oidc verifier init failed; auth middleware will reject all requests", "err", err)
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
