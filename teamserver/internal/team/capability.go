package team

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

const (
	capabilityCatalogSchemaVersion   = "mcp-capability-catalog.v1"
	capabilityCatalogPurpose         = "capability_catalog_report"
	capabilityCatalogMaxBodyBytes    = 64 << 10
	capabilityCatalogRefreshInterval = 5 * time.Minute
	capabilityCatalogRetryBase       = 30 * time.Second
	capabilityCatalogRetryMax        = 5 * time.Minute
	capabilityCatalogAttemptTimeout  = 10 * time.Second
)

var capabilityCatalogIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.\/-]{1,128}$`)

type capabilityCatalogTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// capabilityCatalog is a read-only directory fact. It is not an OAuth
// policy, and reporting it never grants an employee, AI principal or task any
// permission to call a tool.
type capabilityCatalog struct {
	SchemaVersion         string                  `json:"schema_version"`
	MCPInstanceID         string                  `json:"mcp_instance_id,omitempty"`
	BindingID             string                  `json:"binding_id"`
	Source                string                  `json:"source"`
	PublicResource        string                  `json:"public_resource"`
	AuthorizationResource string                  `json:"authorization_resource"`
	CatalogVersion        string                  `json:"catalog_version"`
	Tools                 []capabilityCatalogTool `json:"tools"`
	Scopes                []string                `json:"scopes"`
	Digest                string                  `json:"catalog_digest"`
}

type capabilityCatalogReport struct {
	SchemaVersion         string                  `json:"schema_version"`
	MCPInstanceID         string                  `json:"mcp_instance_id,omitempty"`
	BindingID             string                  `json:"binding_id"`
	Source                string                  `json:"source"`
	PublicResource        string                  `json:"public_resource"`
	AuthorizationResource string                  `json:"authorization_resource"`
	CatalogVersion        string                  `json:"catalog_version"`
	CatalogDigest         string                  `json:"catalog_digest"`
	Tools                 []capabilityCatalogTool `json:"tools"`
	Scopes                []string                `json:"scopes"`
	RequestID             string                  `json:"request_id"`
	Purpose               string                  `json:"purpose"`
}

type capabilityCatalogReportResponse struct {
	Accepted      bool   `json:"accepted"`
	PolicyVersion string `json:"policy_version"`
	CatalogDigest string `json:"catalog_digest"`
	SchemaVersion int    `json:"schema_version"`
	MCPInstanceID string `json:"mcp_instance_id"`
}

// CapabilityCatalog returns the exact plugin-derived definitions already used
// by this Service's tools/list and tools/call paths. It intentionally does not
// inspect the legacy static tool slice or APIWhitelist.
func (s *Service) CapabilityCatalog() (capabilityCatalog, error) {
	if s == nil {
		return capabilityCatalog{}, fmt.Errorf("MCP capability catalog service is unavailable")
	}
	cfg := s.config
	if !cfg.CapabilityCatalogPushEnabled {
		return capabilityCatalog{}, fmt.Errorf("MCP capability catalog reporting is disabled")
	}
	runtime, err := s.capabilityRuntime()
	if err != nil {
		return capabilityCatalog{}, err
	}
	if err := validateCapabilityCatalogMetadata(cfg, runtime); err != nil {
		return capabilityCatalog{}, err
	}
	tools, err := capabilityCatalogTools(s.definitions)
	if err != nil {
		return capabilityCatalog{}, err
	}
	scopes, err := normalizeCapabilityValues("scopes", cfg.RequiredScopes, false)
	if err != nil {
		return capabilityCatalog{}, err
	}
	if len(scopes) == 0 {
		return capabilityCatalog{}, fmt.Errorf("MCP capability catalog requires real OAuth/plugin scopes")
	}
	catalog := capabilityCatalog{
		SchemaVersion:         capabilityCatalogSchemaVersion,
		MCPInstanceID:         cfg.CapabilityCatalogInstanceID,
		BindingID:             cfg.CapabilityCatalogBindingID,
		Source:                cfg.CapabilityCatalogSource,
		PublicResource:        cfg.CapabilityCatalogPublicResource,
		AuthorizationResource: cfg.CapabilityCatalogAuthorizationResource,
		CatalogVersion:        "teamserver-" + serverVersion + "-" + runtime.Digest(),
		Tools:                 tools,
		Scopes:                scopes,
	}
	catalog.Digest = capabilityCatalogDigest(catalog)
	return catalog, nil
}

func (s *Service) capabilityRuntime() (instanceconfig.Config, error) {
	if s.config.Runtime != nil {
		return *s.config.Runtime, nil
	}
	if s.config.BoundRuntime != nil {
		return *s.config.BoundRuntime, nil
	}
	if strings.TrimSpace(s.config.InstanceConfigPath) == "" {
		return instanceconfig.Config{}, fmt.Errorf("MCP capability catalog runtime is unavailable")
	}
	runtime, err := instanceconfig.Load(s.config.InstanceConfigPath)
	if err != nil {
		return instanceconfig.Config{}, fmt.Errorf("MCP capability catalog runtime is unavailable")
	}
	return runtime, nil
}

func validateCapabilityCatalogMetadata(cfg Config, runtime instanceconfig.Config) error {
	if !fleetIdentifier.MatchString(cfg.CapabilityCatalogBindingID) || cfg.CapabilityCatalogBindingID != strings.TrimSpace(cfg.CapabilityCatalogBindingID) {
		return fmt.Errorf("MCP capability catalog binding identity is invalid")
	}
	if !capabilityCatalogIdentifier.MatchString(cfg.CapabilityCatalogSource) || cfg.CapabilityCatalogSource != strings.TrimSpace(cfg.CapabilityCatalogSource) {
		return fmt.Errorf("MCP capability catalog source is invalid")
	}
	if runtime.TenantRoute != cfg.CapabilityCatalogSource {
		return fmt.Errorf("MCP capability catalog source does not match the bound runtime")
	}
	if cfg.AuthorizationTenant != "" && cfg.AuthorizationTenant != cfg.CapabilityCatalogBindingID {
		return fmt.Errorf("MCP capability catalog binding does not match authorization tenant")
	}
	if cfg.CapabilityCatalogInstanceID != "" && !fleetIdentifier.MatchString(cfg.CapabilityCatalogInstanceID) {
		return fmt.Errorf("MCP capability catalog instance identity is invalid")
	}
	if err := validateCapabilityHTTPSURL(cfg.CapabilityCatalogPublicResource); err != nil {
		return fmt.Errorf("MCP capability catalog public resource is invalid")
	}
	if cfg.CapabilityCatalogPublicResource != cfg.PublicURL {
		return fmt.Errorf("MCP capability catalog public resource does not match this instance")
	}
	if cfg.CapabilityCatalogAuthorizationResource == "" || cfg.CapabilityCatalogAuthorizationResource != strings.TrimSpace(cfg.CapabilityCatalogAuthorizationResource) {
		return fmt.Errorf("MCP capability catalog authorization resource is invalid")
	}
	if cfg.AuthorizationResource != "" && cfg.CapabilityCatalogAuthorizationResource != cfg.AuthorizationResource {
		return fmt.Errorf("MCP capability catalog authorization resource does not match this instance")
	}
	return nil
}

func capabilityCatalogTools(definitions []legacymcp.ToolDefinition) ([]capabilityCatalogTool, error) {
	if len(definitions) == 0 {
		return nil, fmt.Errorf("MCP capability catalog has no plugin tools")
	}
	result := make([]capabilityCatalogTool, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" || definition.Name != strings.TrimSpace(definition.Name) || strings.ContainsAny(definition.Name, "*?") {
			return nil, fmt.Errorf("MCP capability catalog contains an invalid tool")
		}
		if _, exists := seen[definition.Name]; exists {
			return nil, fmt.Errorf("MCP capability catalog contains a duplicate tool")
		}
		if len(definition.Description) > 4096 {
			return nil, fmt.Errorf("MCP capability catalog tool description is too long")
		}
		seen[definition.Name] = struct{}{}
		result = append(result, capabilityCatalogTool{Name: definition.Name, Description: definition.Description})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func normalizeCapabilityValues(name string, values []string, allowWildcard bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "*?") && !(allowWildcard && value == "*") {
			return nil, fmt.Errorf("MCP capability catalog %s contains an invalid value", name)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("MCP capability catalog %s contains a duplicate value", name)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// capabilityCatalogDigest mirrors GNAS mcpAdminCapabilityDigest exactly.
// request_id and purpose are transport replay controls, not catalog facts.
func capabilityCatalogDigest(catalog capabilityCatalog) string {
	tools := append([]capabilityCatalogTool(nil), catalog.Tools...)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	scopes := append([]string(nil), catalog.Scopes...)
	sort.Strings(scopes)
	payload := struct {
		SchemaVersion         string                  `json:"schema_version"`
		InstanceID            string                  `json:"mcp_instance_id"`
		BindingID             string                  `json:"binding_id"`
		Source                string                  `json:"source"`
		PublicResource        string                  `json:"public_resource"`
		AuthorizationResource string                  `json:"authorization_resource"`
		CatalogVersion        string                  `json:"catalog_version"`
		Tools                 []capabilityCatalogTool `json:"tools"`
		Scopes                []string                `json:"scopes"`
		CatalogDigest         string                  `json:"catalog_digest"`
	}{catalog.SchemaVersion, catalog.MCPInstanceID, catalog.BindingID, catalog.Source, catalog.PublicResource, catalog.AuthorizationResource, catalog.CatalogVersion, tools, scopes, ""}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// PushCapabilityCatalog reports a directory fact over the existing MCP ->
// GNAS Service JWT direction. It fails closed on every transport, identity,
// digest, response, timeout or replay error and never caches a token.
func (s *Service) PushCapabilityCatalog(ctx context.Context) error {
	catalog, err := s.CapabilityCatalog()
	if err != nil {
		return err
	}
	if !s.config.OAuth21ServiceJWT || s.config.AuthorizationTokenEndpoint == "" || s.config.AuthorizationServiceAppID == "" || s.config.AuthorizationServiceAppSecret == "" {
		return fmt.Errorf("MCP capability catalog requires the configured GNAS Service JWT identity")
	}
	endpoint := strings.TrimSpace(s.config.CapabilityCatalogEndpoint)
	if err := validateCapabilityHTTPSURL(endpoint); err != nil {
		return fmt.Errorf("MCP capability catalog endpoint is invalid")
	}
	timeout := s.config.AuthorizationTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider := &GNASServiceJWTProvider{Endpoint: s.config.AuthorizationTokenEndpoint, Client: client, AppID: s.config.AuthorizationServiceAppID, Secret: s.config.AuthorizationServiceAppSecret, now: time.Now}
	token, err := provider.Token(ctx)
	if err != nil {
		return err
	}
	requestID, err := capabilityRequestID()
	if err != nil {
		return fmt.Errorf("MCP capability catalog request id is unavailable")
	}
	report := capabilityCatalogReport{
		SchemaVersion: catalog.SchemaVersion, MCPInstanceID: catalog.MCPInstanceID, BindingID: catalog.BindingID,
		Source: catalog.Source, PublicResource: catalog.PublicResource, AuthorizationResource: catalog.AuthorizationResource,
		CatalogVersion: catalog.CatalogVersion, CatalogDigest: catalog.Digest, Tools: catalog.Tools, Scopes: catalog.Scopes,
		RequestID: requestID, Purpose: capabilityCatalogPurpose,
	}
	return postCapabilityCatalog(ctx, endpoint, token, report, catalog, client)
}

// runCapabilityCatalogReporter keeps the GNAS directory fresh without making
// the MCP data plane depend on the directory. The first report is attempted in
// the background; failures use bounded exponential retry and do not stop
// tools/list or tools/call. GNAS TTL expiry therefore makes only its admin
// catalog unavailable until a later report succeeds.
func runCapabilityCatalogReporter(ctx context.Context, push func(context.Context) error, logger *slog.Logger, refreshInterval, retryBase, retryMax time.Duration) {
	if ctx == nil || push == nil || refreshInterval <= 0 || retryBase <= 0 || retryMax < retryBase {
		return
	}
	nextDelay := time.Duration(0)
	retryDelay := retryBase
	for {
		timer := time.NewTimer(nextDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		attemptContext, cancel := context.WithTimeout(ctx, capabilityCatalogAttemptTimeout)
		err := push(attemptContext)
		cancel()
		if err == nil {
			nextDelay = refreshInterval
			retryDelay = retryBase
		} else {
			if logger != nil {
				// Do not log err: transport errors can contain endpoint details or
				// upstream response text. The report is directory-only and must
				// never expose tokens, keys or identity assertions.
				logger.Warn("MCP capability catalog report unavailable")
			}
			nextDelay = retryDelay
			retryDelay *= 2
			if retryDelay > retryMax {
				retryDelay = retryMax
			}
		}
	}
}

func postCapabilityCatalog(ctx context.Context, endpoint, token string, report capabilityCatalogReport, catalog capabilityCatalog, client *http.Client) error {
	if err := validateCapabilityHTTPSURL(endpoint); err != nil || token == "" || token != strings.TrimSpace(token) {
		return fmt.Errorf("MCP capability catalog request is invalid")
	}
	body, err := json.Marshal(report)
	if err != nil || len(body) > capabilityCatalogMaxBodyBytes {
		return fmt.Errorf("MCP capability catalog request is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("MCP capability catalog request is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Auth-Type", "service_jwt")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := clientCopy.Do(request)
	if err != nil {
		return fmt.Errorf("MCP capability catalog report failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, capabilityCatalogMaxBodyBytes))
		return fmt.Errorf("MCP capability catalog report rejected with HTTP %d", response.StatusCode)
	}
	if err := requireJSONContentType(response.Header.Get("Content-Type")); err != nil {
		return err
	}
	var result capabilityCatalogReportResponse
	if err := decodeStrictJSON(io.LimitReader(response.Body, capabilityCatalogMaxBodyBytes+1), &result); err != nil {
		return fmt.Errorf("MCP capability catalog report readback failed")
	}
	if !result.Accepted || result.CatalogDigest != catalog.Digest || result.PolicyVersion != catalog.CatalogVersion || result.SchemaVersion != 1 || catalog.MCPInstanceID != "" && result.MCPInstanceID != catalog.MCPInstanceID {
		return fmt.Errorf("MCP capability catalog report readback failed")
	}
	return nil
}

func capabilityRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validateCapabilityHTTPSURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
		return fmt.Errorf("URL must use HTTPS")
	}
	return nil
}
