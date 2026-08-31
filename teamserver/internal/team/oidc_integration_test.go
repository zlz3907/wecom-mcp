package team

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestOIDCAuthenticatorVerifiesSignatureAudienceAndRole(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "test-key"
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer.URL, "jwks_uri": issuer.URL + "/jwks",
				"authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &privateKey.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()

	cfg := Config{
		OIDCIssuer: issuer.URL, OIDCAudience: "team-audience", RolesClaim: "realm_access.roles",
		AccessTokenClaim: "token_use", AccessTokenValue: "access",
		ReaderRole: "reader", OperatorRole: "operator", AdminRole: "admin", RequiredScopes: []string{"wecom.mcp"},
		UserAuthorizationEnabled: true, WeComUserIDClaim: "wecom.userid", PrincipalAssertionClaim: "gnas.principal_assertion",
	}
	authenticator, err := NewOIDCAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	valid := signOIDCToken(t, privateKey, keyID, tokenSpec{
		issuer: issuer.URL, subject: "subject-1", audience: "team-audience", roles: []string{"operator"},
		tokenUse: "access", scope: "openid profile wecom.mcp", userid: "CaseSensitiveUserID", principalAssertion: "signed-principal-test", expiry: time.Now().Add(time.Hour),
	})
	info, err := authenticator.Verify(context.Background(), valid, nil)
	if err != nil {
		t.Fatal(err)
	}
	role, ok := roleFromTokenInfo(info)
	if !ok || role != RoleOperator || info.UserID != "subject-1" || info.Extra["wecom_userid"] != "CaseSensitiveUserID" || info.Extra["gnas_principal_assertion"] != "signed-principal-test" {
		t.Fatalf("token info=%#v", info)
	}

	wrongAudience := signOIDCToken(t, privateKey, keyID, tokenSpec{
		issuer: issuer.URL, subject: "subject-1", audience: "other-audience", roles: []string{"operator"}, tokenUse: "access", expiry: time.Now().Add(time.Hour),
	})
	if _, err := authenticator.Verify(context.Background(), wrongAudience, nil); err == nil {
		t.Fatal("wrong audience must be rejected")
	}
	wrongRole := signOIDCToken(t, privateKey, keyID, tokenSpec{
		issuer: issuer.URL, subject: "subject-1", audience: "team-audience", roles: []string{"unrelated"}, tokenUse: "access", userid: "CaseSensitiveUserID", principalAssertion: "signed-principal-test", expiry: time.Now().Add(time.Hour),
	})
	wrongRoleInfo, err := authenticator.Verify(context.Background(), wrongRole, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := roleFromTokenInfo(wrongRoleInfo); ok {
		t.Fatal("unrelated role must not receive an authorized role")
	}

	negativeTokens := map[string]string{
		"wrong issuer":  signOIDCToken(t, privateKey, keyID, tokenSpec{issuer: "https://wrong.example.test", subject: "subject-1", audience: "team-audience", roles: []string{"operator"}, tokenUse: "access", expiry: time.Now().Add(time.Hour)}),
		"expired":       signOIDCToken(t, privateKey, keyID, tokenSpec{issuer: issuer.URL, subject: "subject-1", audience: "team-audience", roles: []string{"operator"}, tokenUse: "access", expiry: time.Now().Add(-time.Hour)}),
		"empty subject": signOIDCToken(t, privateKey, keyID, tokenSpec{issuer: issuer.URL, audience: "team-audience", roles: []string{"operator"}, tokenUse: "access", expiry: time.Now().Add(time.Hour)}),
		"ID token":      signOIDCToken(t, privateKey, keyID, tokenSpec{issuer: issuer.URL, subject: "subject-1", audience: "team-audience", roles: []string{"operator"}, tokenUse: "id", expiry: time.Now().Add(time.Hour)}),
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	negativeTokens["forged signature"] = signOIDCToken(t, otherKey, keyID, tokenSpec{issuer: issuer.URL, subject: "subject-1", audience: "team-audience", roles: []string{"operator"}, tokenUse: "access", expiry: time.Now().Add(time.Hour)})
	for name, raw := range negativeTokens {
		t.Run(name, func(t *testing.T) {
			if _, err := authenticator.Verify(context.Background(), raw, nil); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestOIDCDiscoveryTimesOut(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer issuer.Close()
	started := time.Now()
	_, err := NewOIDCAuthenticator(context.Background(), Config{
		OIDCIssuer: issuer.URL, OIDCAudience: "team-audience", OIDCHTTPTimeout: 50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("hanging OIDC discovery must time out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("OIDC discovery timeout took %s", elapsed)
	}
}

func TestOIDCJWKSTimesOut(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer.URL, "jwks_uri": issuer.URL + "/jwks",
				"authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	cfg := Config{
		OIDCIssuer: issuer.URL, OIDCAudience: "team-audience", OIDCHTTPTimeout: 50 * time.Millisecond,
		AccessTokenClaim: "token_use", AccessTokenValue: "access", RolesClaim: "roles",
		ReaderRole: "reader", OperatorRole: "operator", AdminRole: "admin",
	}
	authenticator, err := NewOIDCAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := signOIDCToken(t, privateKey, "unknown-key", tokenSpec{
		issuer: issuer.URL, subject: "subject-1", audience: "team-audience", roles: []string{"reader"}, tokenUse: "access", expiry: time.Now().Add(time.Hour),
	})
	started := time.Now()
	if _, err := authenticator.Verify(context.Background(), raw, nil); err == nil {
		t.Fatal("hanging JWKS request must time out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("JWKS timeout took %s", elapsed)
	}
}

type tokenSpec struct {
	issuer             string
	subject            string
	audience           string
	roles              []string
	tokenUse           string
	scope              string
	userid             string
	principalAssertion string
	expiry             time.Time
}

func signOIDCToken(t *testing.T, key *rsa.PrivateKey, keyID string, spec tokenSpec) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: spec.issuer, Subject: spec.subject, Audience: jwt.Audience{spec.audience},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(spec.expiry),
	}).Claims(map[string]any{
		"realm_access": map[string]any{"roles": spec.roles},
		"wecom":        map[string]any{"userid": spec.userid},
		"gnas":         map[string]any{"principal_assertion": spec.principalAssertion},
		"scope":        spec.scope,
		"token_use":    spec.tokenUse,
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}
