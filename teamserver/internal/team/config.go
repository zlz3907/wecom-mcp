package team

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	InstanceConfigPath            string
	AuthenticationMode            AuthenticationMode
	ConnectorAPIKey               string
	ListenAddress                 string
	PublicURL                     string
	MCPURL                        string
	MetadataURL                   string
	OIDCIssuer                    string
	OIDCAudience                  string
	AccessTokenClaim              string
	AccessTokenValue              string
	RolesClaim                    string
	ReaderRole                    string
	OperatorRole                  string
	AdminRole                     string
	RequiredScopes                []string
	AdvertisedScopes              []string
	AuditHMACKey                  []byte
	MaxConcurrentTools            int
	OIDCHTTPTimeout               time.Duration
	ShutdownTimeout               time.Duration
	UserAuthorizationEnabled      bool
	AuthorizationEndpoint         string
	AuthorizationTokenEndpoint    string
	AuthorizationServiceAppID     string
	AuthorizationServiceAppSecret string
	AuthorizationTenant           string
	AuthorizationResource         string
	AuthorizationTimeout          time.Duration
	WeComUserIDClaim              string
	PrincipalAssertionClaim       string
}

type AuthenticationMode string

const (
	AuthenticationModeOIDC            AuthenticationMode = "oidc"
	AuthenticationModeConnectorAPIKey AuthenticationMode = "connector_api_key"
)

func LoadConfig(instanceConfigPath, listenAddress string) (Config, error) {
	gnasBaseURL := strings.TrimSpace(os.Getenv("GNAS_BASE_URL"))
	authorizationServiceSecret := os.Getenv("TEAM_MCP_GNAS_SERVICE_APP_SECRET")
	if authorizationServiceSecret == "" {
		authorizationServiceSecret = os.Getenv("GNAS_APP_SECRET")
	}
	cfg := Config{
		InstanceConfigPath:            instanceConfigPath,
		AuthenticationMode:            AuthenticationMode(firstNonEmpty(os.Getenv("TEAM_MCP_AUTH_MODE"), string(AuthenticationModeOIDC))),
		ConnectorAPIKey:               os.Getenv("TEAM_MCP_CONNECTOR_API_KEY"),
		ListenAddress:                 firstNonEmpty(listenAddress, os.Getenv("TEAM_MCP_LISTEN_ADDR"), "127.0.0.1:17801"),
		PublicURL:                     strings.TrimSpace(os.Getenv("TEAM_MCP_PUBLIC_URL")),
		OIDCIssuer:                    strings.TrimSpace(os.Getenv("TEAM_MCP_OIDC_ISSUER")),
		OIDCAudience:                  strings.TrimSpace(os.Getenv("TEAM_MCP_OIDC_AUDIENCE")),
		AccessTokenClaim:              strings.TrimSpace(os.Getenv("TEAM_MCP_ACCESS_TOKEN_CLAIM")),
		AccessTokenValue:              strings.TrimSpace(os.Getenv("TEAM_MCP_ACCESS_TOKEN_VALUE")),
		RolesClaim:                    firstNonEmpty(os.Getenv("TEAM_MCP_ROLES_CLAIM"), "roles"),
		ReaderRole:                    firstNonEmpty(os.Getenv("TEAM_MCP_READER_ROLE"), "wecom-mcp-reader"),
		OperatorRole:                  firstNonEmpty(os.Getenv("TEAM_MCP_OPERATOR_ROLE"), "wecom-mcp-operator"),
		AdminRole:                     firstNonEmpty(os.Getenv("TEAM_MCP_ADMIN_ROLE"), "wecom-mcp-admin"),
		RequiredScopes:                splitList(os.Getenv("TEAM_MCP_REQUIRED_SCOPES")),
		AuditHMACKey:                  []byte(os.Getenv("TEAM_MCP_AUDIT_HMAC_KEY")),
		MaxConcurrentTools:            16,
		OIDCHTTPTimeout:               10 * time.Second,
		ShutdownTimeout:               20 * time.Second,
		UserAuthorizationEnabled:      strings.TrimSpace(os.Getenv("TEAM_MCP_USER_AUTHZ_ENABLED")) == "true",
		AuthorizationEndpoint:         firstNonEmpty(os.Getenv("TEAM_MCP_AUTHZ_RESOLVE_URL"), gnasServiceURL(gnasBaseURL, "/gnas/service/resolveAuthorizationV1")),
		AuthorizationTokenEndpoint:    firstNonEmpty(os.Getenv("TEAM_MCP_GNAS_SERVICE_TOKEN_URL"), gnasServiceURL(gnasBaseURL, "/gnas/service/getJwtToken")),
		AuthorizationServiceAppID:     firstNonEmpty(os.Getenv("TEAM_MCP_GNAS_SERVICE_APP_ID"), os.Getenv("GNAS_APP_ID")),
		AuthorizationServiceAppSecret: authorizationServiceSecret,
		AuthorizationTenant:           strings.TrimSpace(os.Getenv("TEAM_MCP_AUTHZ_TENANT")),
		AuthorizationResource:         strings.TrimSpace(os.Getenv("TEAM_MCP_AUTHZ_RESOURCE")),
		AuthorizationTimeout:          2 * time.Second,
		WeComUserIDClaim:              strings.TrimSpace(os.Getenv("TEAM_MCP_WECOM_USERID_CLAIM")),
		PrincipalAssertionClaim:       strings.TrimSpace(os.Getenv("TEAM_MCP_GNAS_PRINCIPAL_ASSERTION_CLAIM")),
	}
	if cfg.InstanceConfigPath == "" {
		return Config{}, fmt.Errorf("--config is required")
	}
	if err := validatePublicURL(cfg.PublicURL, "TEAM_MCP_PUBLIC_URL"); err != nil {
		return Config{}, err
	}
	normalizedPublicURL, err := normalizeTeamPublicURL(cfg.PublicURL)
	if err != nil {
		return Config{}, err
	}
	cfg.PublicURL = normalizedPublicURL
	if cfg.AuthorizationResource == "" {
		parsedPublicURL, _ := url.Parse(cfg.PublicURL)
		cfg.AuthorizationResource = strings.TrimPrefix(parsedPublicURL.Path, "/")
	}
	if len(cfg.AuditHMACKey) < 32 || placeholderValue(string(cfg.AuditHMACKey)) {
		return Config{}, fmt.Errorf("TEAM_MCP_AUDIT_HMAC_KEY must contain at least 32 bytes")
	}
	if value := strings.TrimSpace(os.Getenv("TEAM_MCP_MAX_CONCURRENT_TOOLS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 256 {
			return Config{}, fmt.Errorf("TEAM_MCP_MAX_CONCURRENT_TOOLS must be an integer from 1 to 256")
		}
		cfg.MaxConcurrentTools = parsed
	}
	switch cfg.AuthenticationMode {
	case AuthenticationModeOIDC:
		if err := validateHTTPSURL(cfg.OIDCIssuer, "TEAM_MCP_OIDC_ISSUER"); err != nil {
			return Config{}, err
		}
		if cfg.OIDCAudience == "" {
			return Config{}, fmt.Errorf("TEAM_MCP_OIDC_AUDIENCE is required")
		}
		if cfg.AccessTokenClaim == "" || cfg.AccessTokenValue == "" {
			return Config{}, fmt.Errorf("TEAM_MCP_ACCESS_TOKEN_CLAIM and TEAM_MCP_ACCESS_TOKEN_VALUE are required")
		}
		if cfg.RolesClaim == "" || cfg.ReaderRole == "" || cfg.OperatorRole == "" || cfg.AdminRole == "" {
			return Config{}, fmt.Errorf("OIDC role claim and role values must not be empty")
		}
		if cfg.ReaderRole == cfg.OperatorRole || cfg.ReaderRole == cfg.AdminRole || cfg.OperatorRole == cfg.AdminRole {
			return Config{}, fmt.Errorf("OIDC role values must be distinct")
		}
	case AuthenticationModeConnectorAPIKey:
		if len(cfg.ConnectorAPIKey) < 32 || strings.TrimSpace(cfg.ConnectorAPIKey) != cfg.ConnectorAPIKey || placeholderValue(cfg.ConnectorAPIKey) {
			return Config{}, fmt.Errorf("TEAM_MCP_CONNECTOR_API_KEY must contain at least 32 canonical bytes")
		}
		if cfg.UserAuthorizationEnabled {
			return Config{}, fmt.Errorf("connector_api_key mode cannot enable per-user authorization")
		}
	default:
		return Config{}, fmt.Errorf("TEAM_MCP_AUTH_MODE must be oidc or connector_api_key")
	}
	if raw := strings.TrimSpace(os.Getenv("TEAM_MCP_USER_AUTHZ_ENABLED")); raw != "" && raw != "true" && raw != "false" {
		return Config{}, fmt.Errorf("TEAM_MCP_USER_AUTHZ_ENABLED must be true or false")
	}
	if cfg.UserAuthorizationEnabled {
		if strings.TrimSpace(os.Getenv("TEAM_MCP_AUTHZ_CACHE_TTL")) != "" || strings.TrimSpace(os.Getenv("TEAM_MCP_AUTHZ_TIMEOUT")) != "" {
			return Config{}, fmt.Errorf("authorization caching and configurable timeout are not supported")
		}
		if err := validateHTTPSURL(cfg.AuthorizationEndpoint, "TEAM_MCP_AUTHZ_RESOLVE_URL"); err != nil {
			return Config{}, err
		}
		if err := validateHTTPSURL(cfg.AuthorizationTokenEndpoint, "TEAM_MCP_GNAS_SERVICE_TOKEN_URL"); err != nil {
			return Config{}, err
		}
		if cfg.AuthorizationServiceAppID == "" || strings.TrimSpace(cfg.AuthorizationServiceAppID) != cfg.AuthorizationServiceAppID {
			return Config{}, fmt.Errorf("TEAM_MCP_GNAS_SERVICE_APP_ID is required and must be canonical")
		}
		if cfg.AuthorizationServiceAppSecret == "" || strings.TrimSpace(cfg.AuthorizationServiceAppSecret) != cfg.AuthorizationServiceAppSecret || strings.Contains(strings.ToUpper(cfg.AuthorizationServiceAppSecret), "REPLACE") {
			return Config{}, fmt.Errorf("TEAM_MCP_GNAS_SERVICE_APP_SECRET is required")
		}
		if cfg.AuthorizationTenant == "" || strings.TrimSpace(cfg.AuthorizationTenant) != cfg.AuthorizationTenant {
			return Config{}, fmt.Errorf("TEAM_MCP_AUTHZ_TENANT is required and must be canonical")
		}
		if cfg.AuthorizationResource == "" || strings.TrimSpace(cfg.AuthorizationResource) != cfg.AuthorizationResource {
			return Config{}, fmt.Errorf("TEAM_MCP_AUTHZ_RESOURCE is required and must be canonical")
		}
		if cfg.WeComUserIDClaim == "" {
			return Config{}, fmt.Errorf("TEAM_MCP_WECOM_USERID_CLAIM is required when user authorization is enabled")
		}
		if cfg.PrincipalAssertionClaim == "" {
			return Config{}, fmt.Errorf("TEAM_MCP_GNAS_PRINCIPAL_ASSERTION_CLAIM is required when user authorization is enabled")
		}
	}
	if err := validateListenAddress(cfg.ListenAddress); err != nil {
		return Config{}, err
	}
	if !listenIsLoopback(cfg.ListenAddress) && os.Getenv("TEAM_MCP_BEHIND_TLS_PROXY") != "true" {
		return Config{}, fmt.Errorf("non-loopback listen requires TEAM_MCP_BEHIND_TLS_PROXY=true")
	}
	cfg.MCPURL = cfg.PublicURL + "/mcp"
	cfg.MetadataURL = protectedResourceMetadataURL(cfg.MCPURL)
	cfg.AdvertisedScopes = append([]string{"openid", "profile"}, cfg.RequiredScopes...)
	slices.Sort(cfg.AdvertisedScopes)
	cfg.AdvertisedScopes = slices.Compact(cfg.AdvertisedScopes)
	return cfg, nil
}

func placeholderValue(value string) bool {
	upper := strings.ToUpper(value)
	return strings.Contains(upper, "REPLACE") || strings.Contains(upper, "PROTECTED_") || strings.Contains(upper, "OWNER_PROVIDED") || strings.Contains(upper, "PLACEHOLDER")
}

// gnasServiceURL derives only the two fixed GNAS service endpoints from the
// same root used by the fixed-route WeCom transport. Returning an empty value
// leaves the existing fail-closed required-value validation in charge.
func gnasServiceURL(baseURL, endpointPath string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return ""
	}
	parsed.Path = endpointPath
	parsed.RawPath = ""
	return parsed.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			result = append(result, field)
		}
	}
	return result
}

func validatePublicURL(value, name string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute URL without credentials, query, or fragment", name)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return fmt.Errorf("%s must use HTTPS except for loopback development", name)
	}
	return nil
}

func normalizeTeamPublicURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("TEAM_MCP_PUBLIC_URL is invalid")
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" || escapedPath == "/" {
		parsed.Path = ""
		parsed.RawPath = ""
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if parsed.RawPath != "" || !strings.HasPrefix(escapedPath, "/") {
		return "", fmt.Errorf("TEAM_MCP_PUBLIC_URL path prefix must be one unescaped instance segment")
	}
	if strings.HasSuffix(escapedPath, "//") {
		return "", fmt.Errorf("TEAM_MCP_PUBLIC_URL path prefix must not contain repeated trailing slashes")
	}
	escapedPath = strings.TrimSuffix(escapedPath, "/")
	segment := strings.TrimPrefix(escapedPath, "/")
	if segment == "" || strings.Contains(segment, "/") || !validInstancePathSegment(segment) {
		return "", fmt.Errorf("TEAM_MCP_PUBLIC_URL path prefix must be one instance segment containing only letters, digits, '_' or '-'")
	}
	parsed.Path = "/" + segment
	parsed.RawPath = ""
	return parsed.String(), nil
}

func protectedResourceMetadataURL(resourceURL string) string {
	parsed, _ := url.Parse(resourceURL)
	parsed.Path = path.Join("/.well-known/oauth-protected-resource", parsed.Path)
	parsed.RawPath = ""
	return parsed.String()
}

func validInstancePathSegment(value string) bool {
	for index, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (index > 0 && (r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return value != ""
}

func validateHTTPSURL(value, name string) error {
	if err := validatePublicURL(value, name); err != nil {
		return err
	}
	parsed, _ := url.Parse(value)
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("%s must use HTTPS", name)
	}
	return nil
}

func validateListenAddress(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return fmt.Errorf("listen address must be host:port")
	}
	if host != "" && net.ParseIP(host) == nil && host != "localhost" {
		return fmt.Errorf("listen host must be an IP address or localhost")
	}
	return nil
}

func listenIsLoopback(value string) bool {
	host, _, err := net.SplitHostPort(value)
	return err == nil && isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
