package team

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

type ConnectorAPIKeyAuthenticator struct {
	key  []byte
	role Role
}

func NewConnectorAPIKeyAuthenticator(cfg Config) (*ConnectorAPIKeyAuthenticator, error) {
	if cfg.AuthenticationMode != AuthenticationModeConnectorAPIKey || len(cfg.ConnectorAPIKey) < 32 {
		return nil, fmt.Errorf("connector API key authentication is not configured")
	}
	if cfg.ConnectorRole != RoleReader && cfg.ConnectorRole != RoleOperator && cfg.ConnectorRole != RoleAdmin {
		return nil, fmt.Errorf("connector API key role is not configured")
	}
	return &ConnectorAPIKeyAuthenticator{key: []byte(cfg.ConnectorAPIKey), role: cfg.ConnectorRole}, nil
}

func (a *ConnectorAPIKeyAuthenticator) Verify(_ context.Context, rawToken string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	provided := []byte(rawToken)
	if len(provided) != len(a.key) || subtle.ConstantTimeCompare(provided, a.key) != 1 {
		return nil, fmt.Errorf("%w: connector API key rejected", sdkauth.ErrInvalidToken)
	}
	return &sdkauth.TokenInfo{
		UserID:     "workbuddy-enterprise-connector",
		Expiration: time.Now().Add(5 * time.Minute),
		Extra:      map[string]any{"issuer": "workbuddy_connector_api_key", "role": string(a.role)},
	}, nil
}

type oidcTokenVerifier interface {
	Verify(context.Context, string) (*oidc.IDToken, error)
}

type OIDCAuthenticator struct {
	verifier oidcTokenVerifier
	config   Config
}

func NewOIDCAuthenticator(ctx context.Context, cfg Config) (*OIDCAuthenticator, error) {
	timeout := cfg.OIDCHTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	discoveryContext, cancel := context.WithTimeout(oidc.ClientContext(ctx, httpClient), timeout)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryContext, cfg.OIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC issuer: %w", err)
	}
	return &OIDCAuthenticator{
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDCAudience}),
		config:   cfg,
	}, nil
}

func (a *OIDCAuthenticator) Verify(ctx context.Context, rawToken string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	token, err := a.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("%w: bearer token rejected", sdkauth.ErrInvalidToken)
	}
	var claims map[string]json.RawMessage
	if err := token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: bearer token claims rejected", sdkauth.ErrInvalidToken)
	}
	accessTokenValues, err := claimStrings(claims, a.config.AccessTokenClaim)
	if err != nil || !slices.Contains(accessTokenValues, a.config.AccessTokenValue) {
		return nil, fmt.Errorf("%w: access token discriminator rejected", sdkauth.ErrInvalidToken)
	}
	if strings.TrimSpace(token.Subject) == "" {
		return nil, fmt.Errorf("%w: stable token subject required", sdkauth.ErrInvalidToken)
	}
	claimedRoles, err := claimStrings(claims, a.config.RolesClaim)
	if err != nil {
		return nil, fmt.Errorf("%w: team roles claim rejected", sdkauth.ErrInvalidToken)
	}
	scopes := tokenScopes(claims)
	extra := map[string]any{"issuer": a.config.OIDCIssuer}
	if a.config.UserAuthorizationEnabled {
		userids, useridErr := claimStrings(claims, a.config.WeComUserIDClaim)
		if useridErr != nil || len(userids) != 1 || userids[0] == "" || strings.TrimSpace(userids[0]) != userids[0] {
			return nil, fmt.Errorf("%w: unique enterprise WeCom userid claim required", sdkauth.ErrInvalidToken)
		}
		extra["wecom_userid"] = userids[0]
		assertions, assertionErr := claimStrings(claims, a.config.PrincipalAssertionClaim)
		if assertionErr != nil || len(assertions) != 1 || assertions[0] == "" || strings.TrimSpace(assertions[0]) != assertions[0] || len(assertions[0]) > authorizationBodyMaxBytes {
			return nil, fmt.Errorf("%w: unique GNAS principal assertion claim required", sdkauth.ErrInvalidToken)
		}
		extra["gnas_principal_assertion"] = assertions[0]
	}
	if role, roleErr := resolveRole(claimedRoles, a.config); roleErr == nil {
		extra["role"] = string(role)
	}
	return &sdkauth.TokenInfo{
		Scopes:     scopes,
		Expiration: token.Expiry,
		UserID:     token.Subject,
		Extra:      extra,
	}, nil
}

func claimStrings(claims map[string]json.RawMessage, path string) ([]string, error) {
	parts := strings.Split(path, ".")
	current := claims
	for index, part := range parts {
		raw, ok := current[part]
		if !ok {
			return nil, nil
		}
		if index == len(parts)-1 {
			var values []string
			if err := json.Unmarshal(raw, &values); err == nil {
				return values, nil
			}
			var value string
			if err := json.Unmarshal(raw, &value); err == nil {
				return splitList(value), nil
			}
			return nil, fmt.Errorf("claim must be a string or string array")
		}
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, fmt.Errorf("nested claim must be an object")
		}
	}
	return nil, nil
}

func tokenScopes(claims map[string]json.RawMessage) []string {
	for _, name := range []string{"scope", "scp"} {
		if values, err := claimStrings(claims, name); err == nil && len(values) > 0 {
			return values
		}
	}
	return nil
}

func roleFromTokenInfo(info *sdkauth.TokenInfo) (Role, bool) {
	if info == nil || info.Extra == nil {
		return "", false
	}
	value, ok := info.Extra["role"].(string)
	role := Role(value)
	return role, ok && roleRank(role) > 0
}
