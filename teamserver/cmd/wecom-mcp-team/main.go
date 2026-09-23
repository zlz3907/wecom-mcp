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
	gnasFleetRuntimePath := flag.String("gnas-fleet-runtime", "", "absolute local runtime mapping for bindings resolved from GNAS")
	discoveryPolicy := flag.String("gnas-discovery-policy", "", "absolute shared capability policy; discovers unmapped GNAS instances; combine with --gnas-fleet-runtime to preserve protected local instances")
	staticRecoveryURL := flag.String("gnas-static-only", "", "recover one protected runtime mapping at this exact public URL; retains Service JWT and authoritative revocation")
	stateRoot := flag.String("gnas-state-root", "", "existing dedicated absolute state directory for database discovery")
	gnasFleetRefresh := flag.Duration("gnas-fleet-refresh", 0, "GNAS refresh interval (minimum 5s); discovery defaults to 30s, runtime manifest defaults to disabled")
	listenAddress := flag.String("listen", "", "listen address; defaults to TEAM_MCP_LISTEN_ADDR or 127.0.0.1:17801")
	checkConfig := flag.Bool("check-config", false, "validate configuration and initialize local handlers without listening")
	flag.Parse()
	refreshExplicit, staticRecoveryExplicit := false, false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "gnas-static-only" {
			staticRecoveryExplicit = true
		}
		if f.Name == "gnas-fleet-refresh" {
			refreshExplicit = true
		}
	})
	if *discoveryPolicy != "" && !refreshExplicit {
		*gnasFleetRefresh = 30 * time.Second
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if staticRecoveryExplicit && (*staticRecoveryURL == "" || *discoveryPolicy == "" || *gnasFleetRuntimePath == "") {
		logger.Error("static recovery requires hybrid discovery configuration")
		os.Exit(2)
	}
	if *gnasFleetRefresh < 0 || *gnasFleetRefresh > 0 && ((*gnasFleetRuntimePath == "" && *discoveryPolicy == "") || *gnasFleetRefresh < 5*time.Second) || *discoveryPolicy != "" && (*stateRoot == "" || *gnasFleetRefresh == 0) || *stateRoot != "" && *discoveryPolicy == "" {
		logger.Error("invalid GNAS fleet refresh interval or mode")
		os.Exit(2)
	}
	configuredModes := 0
	for _, value := range []string{*configPath, *fleetPath, *gnasFleetRuntimePath, *discoveryPolicy} {
		if value != "" {
			configuredModes++
		}
	}
	if *discoveryPolicy != "" && *gnasFleetRuntimePath != "" {
		configuredModes--
	}
	if configuredModes != 1 {
		logger.Error("invalid team MCP configuration", "error", "exactly one of --config, --fleet, --gnas-fleet-runtime or --gnas-discovery-policy is required")
		os.Exit(2)
	}
	var cfg team.Config
	var handler http.Handler
	var err error
	var refreshingFleet *team.RefreshingFleet
	if *discoveryPolicy != "" {
		var discovery *team.GNASDiscovery
		var discoveryErr error
		if *staticRecoveryURL != "" {
			discovery, discoveryErr = team.NewGNASStaticRecovery(*discoveryPolicy, *stateRoot, *gnasFleetRuntimePath, *listenAddress, *staticRecoveryURL)
		} else if *gnasFleetRuntimePath != "" {
			discovery, discoveryErr = team.NewGNASHybridDiscovery(*discoveryPolicy, *stateRoot, *gnasFleetRuntimePath, *listenAddress)
		} else {
			discovery, discoveryErr = team.NewGNASDiscovery(*discoveryPolicy, *stateRoot, *listenAddress)
		}
		if discoveryErr != nil {
			logger.Error("invalid GNAS discovery configuration", "error", discoveryErr)
			os.Exit(2)
		}
		loadContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		refreshingFleet, err = team.NewDiscoveryFleet(loadContext, discovery.ListenAddress(), discovery.Load, func(bindingConfig team.Config) (http.Handler, error) {
			return bindingHandler(bindingConfig, logger.With("binding_id", bindingConfig.AuthorizationTenant))
		})
		cancel()
		if err != nil {
			logger.Error("GNAS discovery initialization failed; check resolver, policy and existing Registry readiness")
			os.Exit(2)
		}
		cfg.ListenAddress, cfg.ShutdownTimeout = discovery.ListenAddress(), 20*time.Second
		handler = refreshingFleet
		logger.Info("team MCP database discovery configured", "interval", gnasFleetRefresh.String())
	} else if *gnasFleetRuntimePath != "" && *gnasFleetRefresh > 0 {
		loadContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		refreshingFleet, err = team.NewRefreshingFleet(loadContext, func(ctx context.Context) ([]team.LoadedFleetBinding, error) {
			bindings, loadErr := team.LoadGNASFleet(ctx, *gnasFleetRuntimePath, *listenAddress)
			if loadErr != nil {
				return nil, loadErr
			}
			for _, binding := range bindings {
				if err := team.CheckRuntimeSource(binding.Config); err != nil {
					return nil, fmt.Errorf("fleet runtime source validation failed")
				}
			}
			return bindings, nil
		}, func(bindingConfig team.Config) (http.Handler, error) {
			return bindingHandler(bindingConfig, logger.With("binding_id", bindingConfig.AuthorizationTenant))
		})
		cancel()
		if err != nil {
			logger.Error("team MCP refreshing fleet initialization failed")
			os.Exit(2)
		}
		cfg.ListenAddress = refreshingFleet.ListenAddress()
		cfg.ShutdownTimeout = 20 * time.Second
		handler = refreshingFleet
		logger.Info("team MCP fleet refresh configured", "interval", gnasFleetRefresh.String())
	} else if *fleetPath != "" || *gnasFleetRuntimePath != "" {
		var bindings []team.LoadedFleetBinding
		var loadErr error
		if *gnasFleetRuntimePath != "" {
			loadContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			bindings, loadErr = team.LoadGNASFleet(loadContext, *gnasFleetRuntimePath, *listenAddress)
			cancel()
		} else {
			bindings, loadErr = team.LoadFleetManifest(*fleetPath, *listenAddress)
		}
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
	signalContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if refreshingFleet != nil {
		refreshFailed := false
		go refreshingFleet.Run(signalContext, *gnasFleetRefresh, func(refreshErr error) {
			if refreshErr != nil {
				logger.Error("team MCP fleet refresh failed; known hosts unavailable until recovery")
			} else if refreshFailed {
				logger.Info("team MCP fleet refresh recovered")
			}
			refreshFailed = refreshErr != nil
		})
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
