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

	"github.com/acul021/scim-outline-adapter/internal/config"
	"github.com/acul021/scim-outline-adapter/internal/extid"
	"github.com/acul021/scim-outline-adapter/internal/outline"
	"github.com/acul021/scim-outline-adapter/internal/scim"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	client := outline.New(cfg.OutlineURL, cfg.OutlineToken)
	client.SuppressInviteEmails = cfg.SuppressInviteEmails
	server := scim.NewServer(client, scim.RoleMap{
		Admin:  cfg.RoleMapAdmin,
		Member: cfg.RoleMapMember,
		Viewer: cfg.RoleMapViewer,
	}, cfg.SCIMToken, cfg.HardDeleteUsers)
	if cfg.UserExternalIDFile != "" {
		store, err := extid.Open(cfg.UserExternalIDFile)
		if err != nil {
			logger.Error("user external id store", "err", err)
			os.Exit(1)
		}
		server.WithExternalIDStore(store)
		logger.Info("user external id mapping enabled", "file", cfg.UserExternalIDFile)
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           accessLog(server.Handler()),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErr:
		logger.Error("server", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// accessLog logs one structured line per request. It deliberately omits the
// Authorization header and request bodies so bearer tokens and PII are not
// written to logs.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
