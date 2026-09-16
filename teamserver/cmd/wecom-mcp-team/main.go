package main

import (
	"context"
	"flag"
	"fmt"
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
	fleetPath := flag.String("fleet", "", "absolute multi-instance fleet manifest path")
	listenAddress := flag.String("listen", "", "listen address; defaults to TEAM_MCP_LISTEN_ADDR or 127.0.0.1:17801")
	checkConfig := flag.Bool("check-config", false, "validate configuration and initialize local handlers without listening")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if (*configPath == "") == (*fleetPath == "") {
		logger.Error("invalid team MCP configuration", "error", "exactly one of --config or --fleet is required")
		os.Exit(2)
	}
	var cfg team.Config
	var handler http.Handler
	var err error
	if *fleetPath != "" {
		bindings, loadErr := team.LoadFleetManifest(*fleetPath, *listenAddress)
		if loadErr != nil {
			logger.Error("invalid team MCP fleet configuration", "error", loadErr)
			os.Exit(2)
		}
		handlers := make(map[string]http.Handler, len(bindings))
		for _, binding := range bindings {
			bindingLogger := logger.With("binding_id", binding.Binding.BindingID)
			handlers[binding.Binding.BindingID], err = bindingHandler(binding.Config, bindingLogger)
			if err != nil {
				logger.Error("team MCP fleet binding initialization failed", "binding_id", binding.Binding.BindingID, "error", err)
				os.Exit(1)
			}
			if *checkConfig {
				if err := team.CheckRuntimeSource(binding.Config); err != nil {
					logger.Error("team MCP fleet binding runtime source invalid", "binding_id", binding.Binding.BindingID, "error", err)
					os.Exit(1)
				}
			}
		}
		handler, err = team.NewHostRouter(bindings, handlers)
		if err != nil {
			logger.Error("team MCP fleet router initialization failed", "error", err)
			os.Exit(1)
		}
		cfg = bindings[0].Config
		logger.Info("team MCP fleet loaded", "bindings", len(bindings))
	} else {
		cfg, err = team.LoadConfig(*configPath, *listenAddress)
		if err != nil {
			logger.Error("invalid team MCP configuration", "error", err)
			os.Exit(2)
		}
		handler, err = bindingHandler(cfg, logger)
		if err != nil {
			logger.Error("team MCP initialization failed", "error", err)
			os.Exit(1)
		}
		if *checkConfig {
			if err := team.CheckRuntimeSource(cfg); err != nil {
				logger.Error("team MCP runtime source invalid", "error", err)
				os.Exit(1)
			}
		}
	}
	if *checkConfig {
		logger.Info("team MCP configuration valid")
		return
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	serverError := make(chan error, 1)
	go func() {
		logger.Info("team MCP listening", "address", cfg.ListenAddress)
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

func bindingHandler(cfg team.Config, logger *slog.Logger) (http.Handler, error) {
	var verifier func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error)
	if cfg.AuthenticationMode == team.AuthenticationModeConnectorAPIKey {
		authenticator, authErr := team.NewConnectorAPIKeyAuthenticator(cfg)
		if authErr != nil {
			return nil, fmt.Errorf("connector API key authentication initialization: %w", authErr)
		}
		verifier = authenticator.Verify
	} else if cfg.AuthenticationMode == team.AuthenticationModeOAuth21 {
		authenticator, authErr := team.NewOAuth21IntrospectionAuthenticator(cfg)
		if authErr != nil {
			return nil, fmt.Errorf("OAuth 2.1 introspection initialization: %w", authErr)
		}
		verifier = authenticator.Verify
	} else {
		authenticator, authErr := team.NewOIDCAuthenticator(context.Background(), cfg)
		if authErr != nil {
			return nil, fmt.Errorf("OIDC initialization: %w", authErr)
		}
		verifier = authenticator.Verify
	}
	var service *team.Service
	var err error
	if cfg.UserAuthorizationEnabled && cfg.AuthenticationMode != team.AuthenticationModeOAuth21 {
		resolver, resolverErr := team.NewGNASAuthorizationResolver(cfg)
		if resolverErr != nil {
			return nil, fmt.Errorf("GNAS authorization adapter initialization: %w", resolverErr)
		}
		service, err = team.NewServiceWithAuthorizationResolver(cfg, logger, resolver)
	} else {
		service, err = team.NewService(cfg, logger)
	}
	if err != nil {
		return nil, err
	}
	return service.Handler(verifier), nil
}
