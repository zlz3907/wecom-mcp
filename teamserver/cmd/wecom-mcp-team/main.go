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

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
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
	var verifier func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error)
	if cfg.AuthenticationMode == team.AuthenticationModeConnectorAPIKey {
		authenticator, authErr := team.NewConnectorAPIKeyAuthenticator(cfg)
		if authErr != nil {
			logger.Error("connector API key authentication initialization failed", "error", authErr)
			os.Exit(1)
		}
		verifier = authenticator.Verify
	} else {
		authenticator, authErr := team.NewOIDCAuthenticator(context.Background(), cfg)
		if authErr != nil {
			logger.Error("OIDC initialization failed", "error", authErr)
			os.Exit(1)
		}
		verifier = authenticator.Verify
	}
	var service *team.Service
	if cfg.UserAuthorizationEnabled {
		resolver, resolverErr := team.NewGNASAuthorizationResolver(cfg)
		if resolverErr != nil {
			logger.Error("GNAS authorization adapter initialization failed", "error", resolverErr)
			os.Exit(1)
		}
		service, err = team.NewServiceWithAuthorizationResolver(cfg, logger, resolver)
	} else {
		service, err = team.NewService(cfg, logger)
	}
	if err != nil {
		logger.Error("team MCP initialization failed", "error", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           service.Handler(verifier),
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
