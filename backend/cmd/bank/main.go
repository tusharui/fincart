// Package main implements the mock bank API server.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/handlers"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"github.com/tusharui/payment-gateway/backend/internal/security"
	"github.com/tusharui/payment-gateway/backend/internal/service"
	"github.com/tusharui/payment-gateway/backend/internal/webhook"
)

func main() {
	// Load .env for local development. Real environment variables (Docker,
	// CI, shell) always take precedence, and a missing file is fine.
	for _, path := range []string{".env", "../.env"} {
		if err := godotenv.Load(path); err == nil {
			break
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger := cfg.Logger.NewLogger()
	slog.SetDefault(logger)

	logger.Info("starting bank api",
		"port", cfg.Server.Port,
		"log_level", cfg.Logger.Level,
	)

	ctx := context.Background()
	database, err := db.Connect(ctx, &cfg.Database, logger)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err = database.Close(); err != nil {
			logger.Error("failed to close database connection", "error", err)
		}
	}()

	if err := configureAuth(ctx, cfg, database, logger); err != nil {
		logger.Error("failed to configure api authentication", "error", err)
		os.Exit(1)
	}

	// Stop background workers before the database connection is closed.
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	var webhookWorkerDone chan struct{}
	if cfg.Webhook.Enabled {
		dispatcher := webhook.NewDispatcher(database.GORM(), cfg.Webhook, logger)
		webhookWorkerDone = make(chan struct{})
		go func() {
			defer close(webhookWorkerDone)
			dispatcher.Run(workerCtx)
		}()
	}

	// Start periodic cleanup goroutine
	stopCleanup := make(chan struct{})
	go runPeriodicCleanup(database, logger, stopCleanup)

	router := handlers.NewRouter(database, cfg, logger)

	server := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	go func() {
		logger.Info("server listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server...")
	close(stopCleanup)
	stopWorkers()
	if webhookWorkerDone != nil {
		<-webhookWorkerDone
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", "error", err)
	}

	logger.Info("server stopped")
}

// configureAuth reconciles the bootstrap API key. A configured key is stable
// across restarts; otherwise an existing key is reused. On a brand-new database
// with no keys and no bootstrap configuration, a random key is created and
// logged once so the API is not locked out.
func configureAuth(ctx context.Context, cfg *config.Config, database *db.DB, logger *slog.Logger) error {
	if !cfg.Auth.Enabled {
		logger.Warn("api key authentication is disabled", "hint", "set AUTH_ENABLED=true to require API keys")
		return nil
	}

	authService := service.NewAuthService(database, cfg.Auth.LastUsedThrottle, logger)

	if cfg.Auth.BootstrapKey != "" {
		key, err := authService.EnsureBootstrap(ctx, cfg.Auth.BootstrapKey, cfg.Auth.BootstrapMerchant)
		if err != nil {
			return err
		}

		logger.Info("bootstrap api key reconciled",
			"key_id", key.ID,
			"prefix", key.KeyPrefix,
			"merchant_id", key.MerchantID,
		)
		return nil
	}

	activeKeys, err := authService.ActiveKeyCount(ctx)
	if err != nil {
		return err
	}
	if activeKeys > 0 {
		logger.Info("api key authentication enabled", "active_keys", activeKeys)
		return nil
	}

	generated, err := security.GenerateAPIKey()
	if err != nil {
		return err
	}

	key, err := authService.EnsureBootstrap(ctx, generated.Raw, cfg.Auth.BootstrapMerchant)
	if err != nil {
		return err
	}

	logger.Warn("generated one-time bootstrap api key because no active key exists",
		"api_key", generated.Raw,
		"key_id", key.ID,
		"hint", "set AUTH_BOOTSTRAP_KEY to keep the bootstrap credential stable",
	)

	return nil
}

// cleanupIdempotencyKeys removes idempotency keys older than 24 hours
func cleanupIdempotencyKeys(ctx context.Context, database *db.DB, logger *slog.Logger) {
	cutoffTime := time.Now().Add(-24 * time.Hour)
	// Cleanup spans every tenant, so the merchant binding is irrelevant here.
	repo := repository.NewIdempotencyRepository(database.GORM(), uuid.Nil)
	rowsDeleted, err := repo.DeleteOlderThan(ctx, cutoffTime)
	if err != nil {
		logger.Warn("failed to cleanup old idempotency keys", "error", err)
		return
	}
	if rowsDeleted > 0 {
		logger.Info("cleaned up old idempotency keys", "rows_deleted", rowsDeleted)
	}
}

// sweepExpiredAuthorizations releases funds held by authorizations that were
// never captured or voided before their expiry time.
func sweepExpiredAuthorizations(ctx context.Context, database *db.DB, logger *slog.Logger) {
	expiryService := service.NewExpiryService(database)

	expiredCount, err := expiryService.ExpireAuthorizations(ctx, time.Now())
	if err != nil {
		logger.Warn("authorization expiry sweep completed with errors",
			"error", err,
			"expired", expiredCount,
		)
		return
	}
	if expiredCount > 0 {
		logger.Info("expired abandoned authorizations", "count", expiredCount)
	}
}

const (
	// authorizationSweepInterval controls how often expired authorization
	// holds are released.
	authorizationSweepInterval = 1 * time.Minute

	// idempotencyCleanupInterval controls how often old idempotency keys
	// are purged.
	idempotencyCleanupInterval = 1 * time.Hour

	// periodicTaskTimeout bounds a single cleanup pass.
	periodicTaskTimeout = 30 * time.Second
)

// runPeriodicCleanup runs the authorization expiry sweep and idempotency key
// cleanup on their own schedules. Both run once at startup so a restart clears
// any backlog immediately.
func runPeriodicCleanup(database *db.DB, logger *slog.Logger, stop <-chan struct{}) {
	sweepTicker := time.NewTicker(authorizationSweepInterval)
	cleanupTicker := time.NewTicker(idempotencyCleanupInterval)
	defer sweepTicker.Stop()
	defer cleanupTicker.Stop()

	runWithTimeout := func(task func(context.Context)) {
		ctx, cancel := context.WithTimeout(context.Background(), periodicTaskTimeout)
		defer cancel()

		task(ctx)
	}

	runWithTimeout(func(ctx context.Context) {
		sweepExpiredAuthorizations(ctx, database, logger)
	})
	runWithTimeout(func(ctx context.Context) {
		cleanupIdempotencyKeys(ctx, database, logger)
	})

	for {
		select {
		case <-sweepTicker.C:
			runWithTimeout(func(ctx context.Context) {
				sweepExpiredAuthorizations(ctx, database, logger)
			})
		case <-cleanupTicker.C:
			runWithTimeout(func(ctx context.Context) {
				cleanupIdempotencyKeys(ctx, database, logger)
			})
		case <-stop:
			logger.Info("stopping periodic cleanup")
			return
		}
	}
}
