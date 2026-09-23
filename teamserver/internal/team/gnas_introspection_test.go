package team

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGNASIntrospectionUsesServiceIdentityAndChecksTenant(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong_tenant", "wrong_issuer", "wrong_audience", "inactive", "bad_code", "unavailable", "redirect", "stale_binding"} {
		t.Run(scenario, func(t *testing.T) {
			var grants, checks, leaked atomic.Int32
			redirect := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
			defer redirect.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/gnas/service/getJwtToken" {
					grants.Add(1)
					var q map[string]string
					if json.NewDecoder(r.Body).Decode(&q) != nil || q["app_id"] != "test-service" || q["app_secret"] != "test-secret" {
						t.Error("service grant identity mismatch")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "test-service-jwt", "app_id": "test-service", "expires_at": time.Now().Add(time.Minute).Unix()}})
					return
				}
				checks.Add(1)
				var q map[string]string
				if r.URL.Path != "/gnas/service/introspectMCPTokenV1" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-service-jwt" || r.Header.Get("X-Auth-Type") != "service_jwt" || json.NewDecoder(r.Body).Decode(&q) != nil || len(q) != 3 || q["binding_id"] != "gm" || q["binding_digest"] != strings.Repeat("a", 64) || q["token"] != "test-access-token" {
					t.Error("binding-scoped introspection request mismatch")
				}
				if scenario == "redirect" {
					http.Redirect(w, r, redirect.URL, 307)
					return
				}
				if scenario == "stale_binding" {
					w.WriteHeader(403)
					return
				}
				if scenario == "unavailable" {
					w.WriteHeader(503)
					return
				}
				result := map[string]any{"active": true, "iss": "https://jyiai.com/gnas/oauth", "sub": "wecom:gm:member-001", "aud": "https://mcp.jyiai.com/gmzoop/mcp", "exp": time.Now().Add(time.Minute).Unix(), "scope": "zoop.read", "token_use": "access", "tenant": "gm", "wecom_userid": "member-001", "mcp_role": "member", "effective_tools": []string{"wecom_schema_status"}}
				code := 200
				switch scenario {
				case "wrong_tenant":
					result["tenant"] = "other"
				case "wrong_issuer":
					result["iss"] = "https://other.example/gnas/oauth"
				case "wrong_audience":
					result["aud"] = "https://other.example/mcp"
				case "inactive":
					result["active"] = false
				case "bad_code":
					code = 40301
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": result})
			}))
			defer server.Close()
			cfg := testOAuth21Config(server.URL + "/gnas/service/introspectMCPTokenV1")
			cfg.OAuth21ServiceJWT = true
			cfg.GNASBindingDigest = strings.Repeat("a", 64)
			cfg.OAuth21ClientID, cfg.OAuth21ClientSecret = "", ""
			cfg.AuthorizationTokenEndpoint = server.URL + "/gnas/service/getJwtToken"
			cfg.AuthorizationServiceAppID, cfg.AuthorizationServiceAppSecret = "test-service", "test-secret"
			a, err := NewOAuth21IntrospectionAuthenticator(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				_, err = a.Verify(t.Context(), "test-access-token", nil)
				if (err == nil) != (scenario == "valid") {
					t.Fatalf("scenario=%s err=%v", scenario, err)
				}
			}
			if grants.Load() != 2 || checks.Load() != 2 || leaked.Load() != 0 {
				t.Fatal("lookup cached, skipped or redirected credentials")
			}
		})
	}
}
