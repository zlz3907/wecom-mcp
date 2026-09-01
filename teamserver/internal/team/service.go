package team

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

const serverVersion = "0.1.2"

const serverInstructions = "Fixed-tenant Enterprise WeCom Smart Sheet service. Tool visibility and calls are restricted by the authenticated connector role. Before an operator or admin call that needs a human business actor, if the current Enterprise WeCom userid is not explicit in the conversation, ask the user for their name and resolve exactly one active Z-S09 subject. Never use workbuddy-enterprise-connector as a Zoop business actor."

type Service struct {
	config                Config
	legacy                *legacymcp.Server
	definitions           []legacymcp.ToolDefinition
	auditor               *Auditor
	logger                *slog.Logger
	serversMu             sync.Mutex
	servers               map[string]*sdkmcp.Server
	toolSlots             chan struct{}
	authorizationResolver AuthorizationResolver
}

func NewService(cfg Config, logger *slog.Logger) (*Service, error) {
	return newService(cfg, logger, nil)
}

func NewServiceWithAuthorizationResolver(cfg Config, logger *slog.Logger, resolver AuthorizationResolver) (*Service, error) {
	return newService(cfg, logger, resolver)
}

func newService(cfg Config, logger *slog.Logger, resolver AuthorizationResolver) (*Service, error) {
	if len(cfg.AuditHMACKey) < 32 {
		return nil, fmt.Errorf("audit HMAC key must contain at least 32 bytes")
	}
	if cfg.MaxConcurrentTools <= 0 {
		return nil, fmt.Errorf("max concurrent tool calls must be positive")
	}
	definitions, err := legacymcp.ToolDefinitions()
	if err != nil {
		return nil, err
	}
	if cfg.UserAuthorizationEnabled {
		if resolver == nil {
			return nil, fmt.Errorf("user authorization is enabled but the GNAS resolver adapter is not configured")
		}
	}
	return &Service{
		config:                cfg,
		legacy:                legacymcp.New(cfg.InstanceConfigPath),
		definitions:           definitions,
		auditor:               NewAuditor(logger, cfg.AuditHMACKey),
		logger:                logger,
		servers:               make(map[string]*sdkmcp.Server),
		toolSlots:             make(chan struct{}, cfg.MaxConcurrentTools),
		authorizationResolver: resolver,
	}, nil
}

func (s *Service) Handler(verifier sdkauth.TokenVerifier) http.Handler {
	streamable := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server {
		role, ok := roleFromTokenInfo(sdkauth.TokenInfoFromContext(r.Context()))
		if !ok {
			return nil
		}
		return s.serverForRequest(r.Context(), role)
	}, &sdkmcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          2 << 20,
		PropagateRequestCancellation: true,
		CrossOriginProtection:        &http.CrossOriginProtection{},
		Logger:                       s.logger,
	})

	requireOptions := &sdkauth.RequireBearerTokenOptions{
		Scopes:    s.config.RequiredScopes,
		ClockSkew: 30 * time.Second,
	}
	if s.config.AuthenticationMode != AuthenticationModeConnectorAPIKey {
		requireOptions.ResourceMetadataURL = s.config.MetadataURL
	}
	protected := sdkauth.RequireBearerToken(verifier, requireOptions)(requireRole(s.authorizationMiddleware(streamable)))

	mux := http.NewServeMux()
	mux.Handle("/mcp", protected)
	if s.config.AuthenticationMode != AuthenticationModeConnectorAPIKey {
		metadata := &oauthex.ProtectedResourceMetadata{
			Resource:               s.config.MCPURL,
			AuthorizationServers:   []string{s.config.OIDCIssuer},
			ScopesSupported:        s.config.AdvertisedScopes,
			BearerMethodsSupported: []string{"header"},
			ResourceName:           "Guomai Aite WeCom Team MCP",
		}
		mux.Handle("/.well-known/oauth-protected-resource", sdkauth.ProtectedResourceMetadataHandler(metadata))
	}
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.ready)
	return requestIDMiddleware(securityHeaders(mux))
}

func (s *Service) serverForRole(role Role) *sdkmcp.Server {
	return s.serverForTools(role, nil)
}

func (s *Service) serverForRequest(ctx context.Context, role Role) *sdkmcp.Server {
	if !s.config.UserAuthorizationEnabled {
		return s.serverForRole(role)
	}
	authorization, ok := requestAuthorizationFromContext(ctx)
	if !ok {
		return nil
	}
	return s.serverForTools(role, authorization.tools)
}

func (s *Service) serverForTools(role Role, authorized map[string]bool) *sdkmcp.Server {
	key := string(role)
	if authorized != nil {
		names := make([]string, 0, len(authorized))
		for name := range authorized {
			names = append(names, name)
		}
		sort.Strings(names)
		key += "\x00" + strings.Join(names, "\x00")
	}
	s.serversMu.Lock()
	defer s.serversMu.Unlock()
	if existing := s.servers[key]; existing != nil {
		return existing
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "guomai-aite-wecom-team-mcp",
		Version: serverVersion,
	}, &sdkmcp.ServerOptions{
		Instructions: serverInstructions,
		Logger:       s.logger,
	})
	for _, definition := range s.definitions {
		if !allows(role, definition.Access) {
			continue
		}
		if authorized != nil && !authorized[definition.Name] {
			continue
		}
		definition := definition
		readOnly := definition.Access == legacymcp.ToolAccessReader
		server.AddTool(&sdkmcp.Tool{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: definition.InputSchema,
			Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: readOnly},
		}, func(ctx context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return s.callTool(ctx, request, role, definition)
		})
	}
	s.servers[key] = server
	return server
}

func (s *Service) callTool(ctx context.Context, request *sdkmcp.CallToolRequest, role Role, definition legacymcp.ToolDefinition) (*sdkmcp.CallToolResult, error) {
	if !allows(role, definition.Access) {
		return nil, fmt.Errorf("tool is not authorized for this team role")
	}
	if s.config.UserAuthorizationEnabled {
		authorization, ok := requestAuthorizationFromContext(ctx)
		if !ok || !authorization.tools[definition.Name] {
			err := fmt.Errorf("tool is not authorized for this user policy")
			s.auditor.ToolCall(ctx, definition.Name, role, time.Now(), err)
			return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}}}, nil
		}
	}
	started := time.Now()
	select {
	case s.toolSlots <- struct{}{}:
		defer func() { <-s.toolSlots }()
	case <-ctx.Done():
		s.auditor.ToolCall(ctx, definition.Name, role, started, ctx.Err())
		return nil, ctx.Err()
	default:
		err := fmt.Errorf("team MCP tool concurrency limit reached")
		s.auditor.ToolCall(ctx, definition.Name, role, started, err)
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		}, nil
	}
	arguments := json.RawMessage(`{}`)
	if request != nil && request.Params != nil && len(request.Params.Arguments) > 0 {
		arguments = request.Params.Arguments
	}
	value, err := s.legacy.CallTool(ctx, definition.Name, arguments)
	s.auditor.ToolCall(ctx, definition.Name, role, started, err)
	if err != nil {
		return &sdkmcp.CallToolResult{
			IsError: true,
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		}, nil
	}
	encoded, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode tool result: %w", marshalErr)
	}
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(encoded)}},
		StructuredContent: value,
	}, nil
}

type requestAuthorizationKey struct{}

type requestAuthorization struct {
	decision AuthorizationDecision
	tools    map[string]bool
}

func requestAuthorizationFromContext(ctx context.Context) (requestAuthorization, bool) {
	value, ok := ctx.Value(requestAuthorizationKey{}).(requestAuthorization)
	return value, ok
}

func (s *Service) authorizationMiddleware(next http.Handler) http.Handler {
	if !s.config.UserAuthorizationEnabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := sdkauth.TokenInfoFromContext(r.Context())
		userid, assertion, ok := authorizationIdentityFromTokenInfo(info)
		if !ok {
			s.auditor.Authorization(r.Context(), AuthorizationDecision{}, fmt.Errorf("mapped enterprise WeCom userid and principal assertion required"))
			http.Error(w, "user authorization denied", http.StatusForbidden)
			return
		}
		query := AuthorizationQuery{Tenant: s.config.AuthorizationTenant, UserID: userid, Resource: s.config.AuthorizationResource, PrincipalAssertion: assertion}
		ctx, cancel := context.WithTimeout(r.Context(), s.config.AuthorizationTimeout)
		defer cancel()
		decision, err := s.authorizationResolver.ResolveAuthorization(ctx, query)
		if err != nil {
			s.auditor.Authorization(r.Context(), AuthorizationDecision{}, err)
			http.Error(w, "user authorization denied", http.StatusForbidden)
			return
		}
		tools, err := authorizedToolSet(decision, s.definitions, s.config.RequiredScopes)
		s.auditor.Authorization(r.Context(), decision, err)
		if err != nil {
			http.Error(w, "user authorization denied", http.StatusForbidden)
			return
		}
		value := requestAuthorization{decision: decision, tools: tools}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestAuthorizationKey{}, value)))
	})
}

func authorizationIdentityFromTokenInfo(info *sdkauth.TokenInfo) (string, string, bool) {
	if info == nil || info.Extra == nil {
		return "", "", false
	}
	userid, ok := info.Extra["wecom_userid"].(string)
	assertion, assertionOK := info.Extra["gnas_principal_assertion"].(string)
	valid := ok && assertionOK && userid != "" && strings.TrimSpace(userid) == userid && assertion != "" && strings.TrimSpace(assertion) == assertion && len(assertion) <= authorizationBodyMaxBytes
	return userid, assertion, valid
}

func requireRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := roleFromTokenInfo(sdkauth.TokenInfoFromContext(r.Context())); !ok {
			http.Error(w, "team role required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Service) health(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, "ok")
}

func (s *Service) ready(w http.ResponseWriter, _ *http.Request) {
	runtime, err := config.LoadBootstrapCandidate(s.config.InstanceConfigPath)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "not_ready")
		return
	}
	if _, err := config.LoadSchema(runtime.SchemaMirrorPath); err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "not_ready")
		return
	}
	if _, err := wecom.NewFromEnvironment(runtime.TenantRoute); err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "not_ready")
		return
	}
	writeStatus(w, http.StatusOK, "ready")
}

func writeStatus(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": value})
}
