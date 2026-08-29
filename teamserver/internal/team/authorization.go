package team

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

const (
	resolveAuthorizationSchemaV1 = 1
	authorizationBodyMaxBytes    = 64 << 10
	serviceJWTMaxBytes           = 8 << 10
)

// AuthorizationQuery is the stable MCP-side lookup key. The transport adapter
// may change when GNAS freezes its endpoint, but these values must never be
// inferred from a display name, phone number, or client-supplied argument.
type AuthorizationQuery struct {
	Tenant             string `json:"tenant"`
	UserID             string `json:"userid"`
	Resource           string `json:"resource"`
	PrincipalAssertion string `json:"principal_assertion"`
}

// AuthorizationDecision is the normalized MCP-side contract. It deliberately
// contains no Mongo identifiers or storage details.
type AuthorizationDecision struct {
	SchemaVersion   int       `json:"schema_version"`
	Tenant          string    `json:"tenant"`
	UserID          string    `json:"userid"`
	Resource        string    `json:"resource"`
	Active          bool      `json:"active"`
	EffectiveScopes []string  `json:"effective_scopes"`
	EffectiveTools  []string  `json:"effective_tools"`
	PolicyVersion   string    `json:"policy_version"`
	EvaluatedAt     time.Time `json:"evaluated_at"`
}

type resolveAuthorizationV1Envelope struct {
	Code int                            `json:"code"`
	Data resolveAuthorizationV1Response `json:"data"`
}

type resolveAuthorizationV1Response struct {
	SchemaVersion   int       `json:"schema_version"`
	Tenant          string    `json:"tenant"`
	UserID          string    `json:"userid"`
	Resource        string    `json:"resource"`
	Active          bool      `json:"active"`
	EffectiveScopes []string  `json:"effective_scopes"`
	EffectiveTools  []string  `json:"effective_tools"`
	PolicyVersion   string    `json:"policy_version"`
	EvaluatedAt     time.Time `json:"evaluated_at"`
}

type AuthorizationResolver interface {
	ResolveAuthorization(context.Context, AuthorizationQuery) (AuthorizationDecision, error)
}

type ServiceJWTProvider interface {
	Token(context.Context) (string, error)
}

type gnasServiceJWTRequest struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

type gnasServiceJWTEnvelope struct {
	Code int `json:"code"`
	Data struct {
		Token     string `json:"token"`
		AppID     string `json:"app_id"`
		ExpiresAt int64  `json:"expires_at"`
	} `json:"data"`
}

// GNASServiceJWTProvider obtains a fresh short-lived Service JWT for each
// authorization resolution. It deliberately does not cache tokens or place
// app credentials in a URL.
type GNASServiceJWTProvider struct {
	Endpoint string
	Client   *http.Client
	AppID    string
	Secret   string
	now      func() time.Time
}

func (p *GNASServiceJWTProvider) Token(ctx context.Context) (string, error) {
	if p == nil || p.Client == nil || p.now == nil {
		return "", fmt.Errorf("GNAS Service JWT provider is not configured")
	}
	body, err := json.Marshal(gnasServiceJWTRequest{AppID: p.AppID, AppSecret: p.Secret})
	if err != nil {
		return "", fmt.Errorf("encode GNAS Service JWT request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create GNAS Service JWT request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := p.Client.Do(request)
	if err != nil {
		return "", fmt.Errorf("obtain GNAS Service JWT: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, authorizationBodyMaxBytes))
		return "", fmt.Errorf("obtain GNAS Service JWT returned HTTP %d", response.StatusCode)
	}
	if err := requireJSONContentType(response.Header.Get("Content-Type")); err != nil {
		return "", err
	}
	var envelope gnasServiceJWTEnvelope
	if err := decodeStrictJSON(response.Body, &envelope); err != nil {
		return "", fmt.Errorf("decode GNAS Service JWT response: %w", err)
	}
	if envelope.Code != http.StatusOK || envelope.Data.AppID != p.AppID || envelope.Data.Token == "" || len(envelope.Data.Token) > serviceJWTMaxBytes || strings.TrimSpace(envelope.Data.Token) != envelope.Data.Token || envelope.Data.ExpiresAt <= p.now().Unix() {
		return "", fmt.Errorf("GNAS Service JWT response contract is invalid")
	}
	return envelope.Data.Token, nil
}

type AuthorizationResolveError struct {
	HTTPStatus   int
	BusinessCode int
}

func (e *AuthorizationResolveError) Error() string {
	return fmt.Sprintf("authorization denied with HTTP %d and business code %d", e.HTTPStatus, e.BusinessCode)
}

// HTTPAuthorizationResolver is an intentionally narrow adapter around the
// candidate ResolveAuthorizationV1 JSON contract. Endpoint authentication is
// injected so the MCP does not guess GNAS's final Service JWT mechanism.
type HTTPAuthorizationResolver struct {
	Endpoint      string
	Client        *http.Client
	TokenProvider ServiceJWTProvider
}

func (r *HTTPAuthorizationResolver) ResolveAuthorization(ctx context.Context, query AuthorizationQuery) (AuthorizationDecision, error) {
	if r == nil || r.Client == nil || r.TokenProvider == nil {
		return AuthorizationDecision{}, fmt.Errorf("authorization HTTP client is not configured")
	}
	parsed, err := url.Parse(r.Endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return AuthorizationDecision{}, fmt.Errorf("authorization endpoint is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return AuthorizationDecision{}, fmt.Errorf("authorization endpoint must use HTTPS except for loopback tests")
	}
	if err := validateAuthorizationQuery(query); err != nil {
		return AuthorizationDecision{}, err
	}
	token, err := r.TokenProvider.Token(ctx)
	if err != nil {
		return AuthorizationDecision{}, err
	}
	body, err := json.Marshal(query)
	if err != nil {
		return AuthorizationDecision{}, fmt.Errorf("encode authorization query: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return AuthorizationDecision{}, fmt.Errorf("create authorization request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Auth-Type", "service_jwt")
	response, err := r.Client.Do(request)
	if err != nil {
		return AuthorizationDecision{}, fmt.Errorf("resolve authorization: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if err := requireJSONContentType(response.Header.Get("Content-Type")); err != nil {
			return AuthorizationDecision{}, err
		}
		var envelope struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := decodeStrictJSON(response.Body, &envelope); err != nil {
			return AuthorizationDecision{}, fmt.Errorf("decode authorization error contract: %w", err)
		}
		if !validAuthorizationError(response.StatusCode, envelope.Code) {
			return AuthorizationDecision{}, fmt.Errorf("authorization error contract is invalid")
		}
		return AuthorizationDecision{}, &AuthorizationResolveError{HTTPStatus: response.StatusCode, BusinessCode: envelope.Code}
	}
	if err := requireJSONContentType(response.Header.Get("Content-Type")); err != nil {
		return AuthorizationDecision{}, err
	}
	var envelope resolveAuthorizationV1Envelope
	if err := decodeStrictJSON(response.Body, &envelope); err != nil {
		return AuthorizationDecision{}, fmt.Errorf("decode authorization decision: %w", err)
	}
	if envelope.Code != http.StatusOK {
		return AuthorizationDecision{}, fmt.Errorf("authorization success envelope code is invalid")
	}
	wire := envelope.Data
	decision := AuthorizationDecision{
		SchemaVersion: wire.SchemaVersion, Tenant: wire.Tenant, UserID: wire.UserID, Resource: wire.Resource,
		Active: wire.Active, EffectiveScopes: wire.EffectiveScopes, EffectiveTools: wire.EffectiveTools,
		PolicyVersion: wire.PolicyVersion, EvaluatedAt: wire.EvaluatedAt,
	}
	if err := validateAuthorizationDecision(query, decision); err != nil {
		return AuthorizationDecision{}, err
	}
	return decision, nil
}

func validateAuthorizationQuery(query AuthorizationQuery) error {
	for name, value := range map[string]string{"tenant": query.Tenant, "userid": query.UserID, "resource": query.Resource} {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("authorization %s must be nonempty and canonical", name)
		}
	}
	if query.PrincipalAssertion == "" || strings.TrimSpace(query.PrincipalAssertion) != query.PrincipalAssertion || len(query.PrincipalAssertion) > authorizationBodyMaxBytes {
		return fmt.Errorf("authorization principal assertion must be nonempty and canonical")
	}
	return nil
}

func validateAuthorizationDecision(query AuthorizationQuery, decision AuthorizationDecision) error {
	if err := validateAuthorizationQuery(query); err != nil {
		return err
	}
	if decision.SchemaVersion != resolveAuthorizationSchemaV1 {
		return fmt.Errorf("authorization schema version is unsupported")
	}
	if decision.Tenant != query.Tenant || decision.UserID != query.UserID || decision.Resource != query.Resource {
		return fmt.Errorf("authorization identity or resource does not match request")
	}
	if decision.PolicyVersion == "" || strings.TrimSpace(decision.PolicyVersion) != decision.PolicyVersion {
		return fmt.Errorf("authorization policy version is required")
	}
	if decision.EvaluatedAt.IsZero() {
		return fmt.Errorf("authorization evaluation time is required")
	}
	if err := validateStringSet("effective scopes", decision.EffectiveScopes, false); err != nil {
		return err
	}
	if err := validateStringSet("effective tools", decision.EffectiveTools, true); err != nil {
		return err
	}
	return nil
}

func validAuthorizationError(status, code int) bool {
	return status == http.StatusUnauthorized && code == 40101 ||
		status == http.StatusForbidden && code == 40301 ||
		status == http.StatusMethodNotAllowed && code == 40501 ||
		status == http.StatusBadRequest && code == 40001 ||
		status == http.StatusServiceUnavailable && code == 50301
}

func requireJSONContentType(value string) error {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("GNAS response content type is not application/json")
	}
	return nil
}

func decodeStrictJSON(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, authorizationBodyMaxBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > authorizationBodyMaxBytes {
		return fmt.Errorf("response body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("response contains trailing JSON")
	}
	return nil
}

func NewGNASAuthorizationResolver(cfg Config) (AuthorizationResolver, error) {
	if !cfg.UserAuthorizationEnabled {
		return nil, fmt.Errorf("user authorization is not enabled")
	}
	resolverURL, err := url.Parse(cfg.AuthorizationEndpoint)
	if err != nil {
		return nil, fmt.Errorf("authorization endpoint is invalid")
	}
	tokenURL, err := url.Parse(cfg.AuthorizationTokenEndpoint)
	if err != nil || resolverURL.Scheme != tokenURL.Scheme || resolverURL.Host != tokenURL.Host {
		return nil, fmt.Errorf("GNAS token and resolver endpoints must use the same origin")
	}
	if resolverURL.Path != "/gnas/service/resolveAuthorizationV1" || tokenURL.Path != "/gnas/service/getJwtToken" {
		return nil, fmt.Errorf("GNAS token and resolver endpoint paths do not match the stable contract")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	provider := &GNASServiceJWTProvider{
		Endpoint: cfg.AuthorizationTokenEndpoint,
		Client:   client,
		AppID:    cfg.AuthorizationServiceAppID,
		Secret:   cfg.AuthorizationServiceAppSecret,
		now:      time.Now,
	}
	return &HTTPAuthorizationResolver{Endpoint: cfg.AuthorizationEndpoint, Client: client, TokenProvider: provider}, nil
}

func validateStringSet(name string, values []string, allowWildcard bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("authorization %s contains an invalid value", name)
		}
		if value == "*" && !allowWildcard {
			return fmt.Errorf("authorization %s does not support wildcard", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("authorization %s contains a duplicate value", name)
		}
		seen[value] = struct{}{}
	}
	if _, wildcard := seen["*"]; wildcard && len(seen) != 1 {
		return fmt.Errorf("authorization effective tools wildcard must be the only value")
	}
	return nil
}

func authorizedToolSet(decision AuthorizationDecision, definitions []legacymcp.ToolDefinition, requiredScopes []string) (map[string]bool, error) {
	if !decision.Active {
		return nil, fmt.Errorf("authorization is inactive")
	}
	if !containsAll(decision.EffectiveScopes, requiredScopes) {
		return nil, fmt.Errorf("authorization is missing required scopes")
	}
	publicTools := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		publicTools[definition.Name] = true
	}
	if slices.Equal(decision.EffectiveTools, []string{"*"}) {
		return publicTools, nil
	}
	result := make(map[string]bool, len(decision.EffectiveTools))
	for _, name := range decision.EffectiveTools {
		if !publicTools[name] {
			return nil, fmt.Errorf("authorization references an unknown MCP tool")
		}
		result[name] = true
	}
	return result, nil
}
