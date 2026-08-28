package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/teamserver/internal/team"
)

func main() {
	configPath := flag.String("config", "", "absolute fixed-tenant instance configuration path")
	listenAddress := flag.String("listen", "", "listen address; defaults to TEAM_MCP_LISTEN_ADDR or 127.0.0.1:17801")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := team.LoadConfig(*configPath, *listenAddress)
	if err != nil {
		logger.Error("invalid team MCP configuration", "error", err)
		os.Exit(2)
	}
	authenticator, err := team.NewOIDCAuthenticator(context.Background(), cfg)
	if err != nil {
		logger.Error("OIDC initialization failed", "error", err)
		os.Exit(1)
	}
	service, err := team.NewService(cfg, logger)
	if err != nil {
		logger.Error("team MCP initialization failed", "error", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           service.Handler(authenticator.Verify),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	serverError := make(chan error, 1)
	go func() {
		logger.Info("team MCP listening", "address", cfg.ListenAddress, "public_url", cfg.PublicURL)
		serverError <- httpServer.ListenAndServe()
	}()

	signalContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverError:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("team MCP stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			logger.Error("team MCP graceful shutdown failed", "error", err)
			os.Exit(1)
		}
		logger.Info("team MCP stopped")
	}
}
