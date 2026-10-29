// Command api is the WalletCore HTTP server entry point.
//
// Wiring happens here by hand: configuration, structured logging, the shared
// database pool, the router and the HTTP server are constructed in main and
// passed to the pieces that need them. There is no global state and no
// framework container.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jaywestty/walletcore/internal/config"
	"github.com/Jaywestty/walletcore/internal/db"
	"github.com/Jaywestty/walletcore/internal/httpapi"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so write to
		// stderr and exit non-zero.
		fmt.Fprintf(os.Stderr, "walletcore: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL, db.LimitsFromConfig(cfg))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	handler := httpapi.NewRouter(httpapi.Options{
		Logger:            logger,
		ReadinessPinger:   pool,
		ReadyTimeout:      2 * time.Second,
		TrustProxyHeaders: cfg.TrustProxyHeaders,
	})

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      handler,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		IdleTimeout:  cfg.HTTPIdleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped cleanly")
	return nil
}

// newLogger builds a structured JSON logger at the configured level.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
