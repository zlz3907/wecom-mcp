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

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

const oauth21IntrospectionResponseMax = 64 << 10

type OAuth21IntrospectionAuthenticator struct {
	config Config
	client *http.Client
}

type oauth21IntrospectionResponse struct {
	Active         bool            `json:"active"`
	Issuer         string          `json:"iss"`
	Subject        string          `json:"sub"`
	Audience       json.RawMessage `json:"aud"`
	ExpiresAt      int64           `json:"exp"`
	Scope          string          `json:"scope"`
	TokenUse       string          `json:"token_use"`
	Tenant         string          `json:"tenant"`
	WeComUserID    string          `json:"wecom_userid"`
	MCPRole        string          `json:"mcp_role"`
	EffectiveTools []string        `json:"effective_tools"`
}

func NewOAuth21IntrospectionAuthenticator(cfg Config) (*OAuth21IntrospectionAuthenticator, error) {
	if cfg.AuthenticationMode != AuthenticationModeOAuth21 || cfg.OAuth21IntrospectionURL == "" || cfg.OAuth21ClientID == "" || len(cfg.OAuth21ClientSecret) < 32 || cfg.OIDCIssuer == "" || cfg.OIDCAudience == "" || cfg.AuthorizationTenant == "" {
		return nil, fmt.Errorf("OAuth 2.1 introspection authentication is not configured")
	}
	timeout := cfg.OIDCHTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &OAuth21IntrospectionAuthenticator{config: cfg, client: &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (a *OAuth21IntrospectionAuthenticator) Verify(ctx context.Context, rawToken string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	if rawToken == "" || rawToken != strings.TrimSpace(rawToken) || len(rawToken) > 16<<10 {
		return nil, fmt.Errorf("%w: bearer token rejected", sdkauth.ErrInvalidToken)
	}
	form := url.Values{}
	form.Set("token", rawToken)
	form.Set("resource", a.config.OIDCAudience)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.OAuth21IntrospectionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: token verification unavailable", sdkauth.ErrInvalidToken)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(a.config.OAuth21ClientID, a.config.OAuth21ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: token verification unavailable", sdkauth.ErrInvalidToken)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: bearer token rejected", sdkauth.ErrInvalidToken)
	}
	mediaType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentTypeErr != nil || mediaType != "application/json" {
		return nil, fmt.Errorf("%w: token verification unavailable", sdkauth.ErrInvalidToken)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, oauth21IntrospectionResponseMax+1))
	if err != nil || len(body) > oauth21IntrospectionResponseMax {
		return nil, fmt.Errorf("%w: token verification unavailable", sdkauth.ErrInvalidToken)
	}
	var result oauth21IntrospectionResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validOAuth21IntrospectionResponse(result, a.config, time.Now()) {
		return nil, fmt.Errorf("%w: bearer token rejected", sdkauth.ErrInvalidToken)
	}
	return &sdkauth.TokenInfo{
		Scopes: splitList(result.Scope), Expiration: time.Unix(result.ExpiresAt, 0), UserID: result.Subject,
		Extra: map[string]any{
			"issuer": result.Issuer, "role": string(RolePolicy), "wecom_userid": result.WeComUserID,
			"mcp_role": result.MCPRole, "effective_tools": append([]string(nil), result.EffectiveTools...),
		},
	}, nil
}

func validOAuth21IntrospectionResponse(result oauth21IntrospectionResponse, cfg Config, now time.Time) bool {
	if !result.Active || result.Issuer != cfg.OIDCIssuer || result.Subject == "" || result.Subject != strings.TrimSpace(result.Subject) || result.ExpiresAt <= now.Unix() || result.TokenUse != "access" || result.Tenant != cfg.AuthorizationTenant || result.WeComUserID == "" || result.WeComUserID != strings.TrimSpace(result.WeComUserID) || result.MCPRole == "" || result.MCPRole != strings.TrimSpace(result.MCPRole) || !oauth21AudienceContains(result.Audience, cfg.OIDCAudience) {
		return false
	}
	scopes := splitList(result.Scope)
	if len(scopes) == 0 || !containsAll(scopes, cfg.RequiredScopes) {
		return false
	}
	if len(result.EffectiveTools) == 0 || len(result.EffectiveTools) > 128 {
		return false
	}
	seen := make(map[string]struct{}, len(result.EffectiveTools))
	for _, tool := range result.EffectiveTools {
		if !validOAuth21ToolName(tool) {
			return false
		}
		if _, exists := seen[tool]; exists {
			return false
		}
		seen[tool] = struct{}{}
	}
	return true
}

func oauth21AudienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	return json.Unmarshal(raw, &multiple) == nil && slices.Contains(multiple, expected)
}

func validOAuth21ToolName(value string) bool {
	if value == "*" {
		return true
	}
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}
