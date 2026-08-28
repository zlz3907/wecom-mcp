package team

import (
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsToLoopbackAndBuildsOAuthURLs(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("TEAM_MCP_PUBLIC_URL", "https://mcp.example.test")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test")
	t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "127.0.0.1:17801" || cfg.MCPURL != "https://mcp.example.test/mcp" {
		t.Fatalf("unexpected endpoints: %#v", cfg)
	}
	if cfg.MetadataURL != "https://mcp.example.test/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadata URL=%q", cfg.MetadataURL)
	}
}

func TestLoadConfigPreservesExactOIDCIssuer(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("TEAM_MCP_PUBLIC_URL", "https://mcp.example.test")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test/tenant/")
	t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDCIssuer != "https://login.example.test/tenant/" {
		t.Fatalf("issuer=%q", cfg.OIDCIssuer)
	}
}

func TestLoadConfigRejectsPublicPlainHTTP(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("TEAM_MCP_PUBLIC_URL", "http://mcp.example.test")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test")
	t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), ""); err == nil {
		t.Fatal("public plain HTTP must be rejected")
	}
}

func TestLoadConfigRequiresExplicitTLSProxyForPublicListen(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("TEAM_MCP_PUBLIC_URL", "https://mcp.example.test")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test")
	t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), "0.0.0.0:17801"); err == nil {
		t.Fatal("public listen without TLS proxy acknowledgement must fail")
	}
	t.Setenv("TEAM_MCP_BEHIND_TLS_PROXY", "true")
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), "0.0.0.0:17801"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigBuildsGMZoopPrefixedOAuthURLs(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("TEAM_MCP_PUBLIC_URL", "https://mcp.jyiai.com/gmzoop/")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test")
	t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	base := "https://mcp.jyiai.com/gmzoop"
	if cfg.PublicURL != base || cfg.MCPURL != base+"/mcp" {
		t.Fatalf("unexpected tenant endpoints: %#v", cfg)
	}
	if cfg.MetadataURL != "https://mcp.jyiai.com/.well-known/oauth-protected-resource/gmzoop/mcp" {
		t.Fatalf("metadata URL=%q", cfg.MetadataURL)
	}
}

func TestLoadConfigRejectsAmbiguousPublicURLPathPrefix(t *testing.T) {
	for _, publicURL := range []string{
		"https://mcp.example.test/gmzoop/nested",
		"https://mcp.example.test/%67mzoop",
		"https://mcp.example.test/-gmzoop",
		"https://mcp.example.test/gmzoop//",
	} {
		t.Run(publicURL, func(t *testing.T) {
			setRequiredConfigEnv(t)
			t.Setenv("TEAM_MCP_PUBLIC_URL", publicURL)
			t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test")
			t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
			if _, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), ""); err == nil {
				t.Fatal("ambiguous public URL path prefix must fail closed")
			}
		})
	}
}

func TestLoadConfigRejectsAuditKeyPlaceholder(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("TEAM_MCP_PUBLIC_URL", "https://mcp.example.test")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://login.example.test")
	t.Setenv("TEAM_MCP_OIDC_AUDIENCE", "wecom-team")
	t.Setenv("TEAM_MCP_AUDIT_HMAC_KEY", "REPLACE_WITH_RANDOM_32_BYTE_OR_LONGER_SECRET")
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "instance.json"), ""); err == nil {
		t.Fatal("published audit key placeholder must fail closed")
	}
}

func setRequiredConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TEAM_MCP_ACCESS_TOKEN_CLAIM", "token_use")
	t.Setenv("TEAM_MCP_ACCESS_TOKEN_VALUE", "access")
	t.Setenv("TEAM_MCP_AUDIT_HMAC_KEY", "0123456789abcdef0123456789abcdef")
}
