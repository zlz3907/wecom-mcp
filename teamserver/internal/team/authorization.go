package team

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

const resolveAuthorizationSchemaV1 = "1"

// AuthorizationQuery is the stable MCP-side lookup key. The transport adapter
// may change when GNAS freezes its endpoint, but these values must never be
// inferred from a display name, phone number, or client-supplied argument.
type AuthorizationQuery struct {
	Tenant   string `json:"tenant"`
	UserID   string `json:"userid"`
	Resource string `json:"resource"`
}

// AuthorizationDecision is the normalized MCP-side contract. It deliberately
// contains no Mongo identifiers or storage details.
type AuthorizationDecision struct {
	SchemaVersion   string    `json:"schema_version"`
	Tenant          string    `json:"tenant"`
	UserID          string    `json:"userid"`
	Resource        string    `json:"-"`
	Active          bool      `json:"active"`
	EffectiveScopes []string  `json:"effective_scopes"`
	EffectiveTools  []string  `json:"effective_tools"`
	PolicyVersion   string    `json:"policy_version"`
	EvaluatedAt     time.Time `json:"evaluated_at"`
}

// resolveAuthorizationV1Response is the candidate GNAS wire fixture. Resource
// remains part of the authenticated request key and is copied into the
// normalized decision, so a future GNAS response-field change is isolated here.
type resolveAuthorizationV1Response struct {
	SchemaVersion   string    `json:"schema_version"`
	Tenant          string    `json:"tenant"`
	UserID          string    `json:"userid"`
	Active          bool      `json:"active"`
	EffectiveScopes []string  `json:"effective_scopes"`
	EffectiveTools  []string  `json:"effective_tools"`
	PolicyVersion   string    `json:"policy_version"`
	EvaluatedAt     time.Time `json:"evaluated_at"`
}

type AuthorizationResolver interface {
	ResolveAuthorization(context.Context, AuthorizationQuery) (AuthorizationDecision, error)
}

type RequestAuthorizer func(*http.Request) error

// HTTPAuthorizationResolver is an intentionally narrow adapter around the
// candidate ResolveAuthorizationV1 JSON contract. Endpoint authentication is
// injected so the MCP does not guess GNAS's final Service JWT mechanism.
type HTTPAuthorizationResolver struct {
	Endpoint  string
	Client    *http.Client
	Authorize RequestAuthorizer
}

func (r *HTTPAuthorizationResolver) ResolveAuthorization(ctx context.Context, query AuthorizationQuery) (AuthorizationDecision, error) {
	if r == nil || r.Client == nil {
		return AuthorizationDecision{}, fmt.Errorf("authorization HTTP client is not configured")
	}
	parsed, err := url.Parse(r.Endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return AuthorizationDecision{}, fmt.Errorf("authorization endpoint is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return AuthorizationDecision{}, fmt.Errorf("authorization endpoint must use HTTPS except for loopback tests")
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
	if r.Authorize == nil {
		return AuthorizationDecision{}, fmt.Errorf("authorization request signer is not configured")
	}
	if err := r.Authorize(request); err != nil {
		return AuthorizationDecision{}, fmt.Errorf("authorize GNAS request: %w", err)
	}
	response, err := r.Client.Do(request)
	if err != nil {
		return AuthorizationDecision{}, fmt.Errorf("resolve authorization: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 64<<10)
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, limited)
		return AuthorizationDecision{}, fmt.Errorf("resolve authorization returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var wire resolveAuthorizationV1Response
	if err := decoder.Decode(&wire); err != nil {
		return AuthorizationDecision{}, fmt.Errorf("decode authorization decision: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return AuthorizationDecision{}, fmt.Errorf("authorization response contains trailing JSON")
	}
	decision := AuthorizationDecision{
		SchemaVersion: wire.SchemaVersion, Tenant: wire.Tenant, UserID: wire.UserID, Resource: query.Resource,
		Active: wire.Active, EffectiveScopes: wire.EffectiveScopes, EffectiveTools: wire.EffectiveTools,
		PolicyVersion: wire.PolicyVersion, EvaluatedAt: wire.EvaluatedAt,
	}
	if err := validateAuthorizationDecision(query, decision); err != nil {
		return AuthorizationDecision{}, err
	}
	return decision, nil
}

type authorizationCacheEntry struct {
	decision AuthorizationDecision
	expires  time.Time
}

type CachedAuthorizationResolver struct {
	upstream AuthorizationResolver
	ttl      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	entries  map[AuthorizationQuery]authorizationCacheEntry
}

func NewCachedAuthorizationResolver(upstream AuthorizationResolver, ttl time.Duration) (*CachedAuthorizationResolver, error) {
	if upstream == nil {
		return nil, fmt.Errorf("authorization resolver is required")
	}
	if ttl <= 0 || ttl > time.Minute {
		return nil, fmt.Errorf("authorization cache TTL must be between 1ns and 1m")
	}
	return &CachedAuthorizationResolver{
		upstream: upstream,
		ttl:      ttl,
		now:      time.Now,
		entries:  make(map[AuthorizationQuery]authorizationCacheEntry),
	}, nil
}

func (r *CachedAuthorizationResolver) ResolveAuthorization(ctx context.Context, query AuthorizationQuery) (AuthorizationDecision, error) {
	now := r.now()
	r.mu.Lock()
	entry, ok := r.entries[query]
	if ok && now.Before(entry.expires) {
		r.mu.Unlock()
		return entry.decision, nil
	}
	if ok {
		delete(r.entries, query)
	}
	r.mu.Unlock()

	decision, err := r.upstream.ResolveAuthorization(ctx, query)
	if err != nil {
		// Never serve stale authorization after an upstream failure.
		return AuthorizationDecision{}, err
	}
	if err := validateAuthorizationDecision(query, decision); err != nil {
		return AuthorizationDecision{}, err
	}
	r.mu.Lock()
	r.entries[query] = authorizationCacheEntry{decision: decision, expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return decision, nil
}

func validateAuthorizationQuery(query AuthorizationQuery) error {
	for name, value := range map[string]string{"tenant": query.Tenant, "userid": query.UserID, "resource": query.Resource} {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("authorization %s must be nonempty and canonical", name)
		}
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
