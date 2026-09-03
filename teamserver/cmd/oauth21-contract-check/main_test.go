package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunChecksOAuthDiscoveryAndChallengeWithoutToken(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/as-metadata":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": server.URL + "/issuer", "authorization_endpoint": server.URL + "/authorize",
				"token_endpoint": server.URL + "/token", "code_challenge_methods_supported": []string{"S256"},
				"client_id_metadata_document_supported": true,
			})
		case "/resource-metadata":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource": server.URL + "/mcp", "authorization_servers": []string{server.URL + "/issuer"},
			})
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/resource-metadata"`)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := run(context.Background(), options{
		issuerURL: server.URL + "/issuer", authorizationMeta: server.URL + "/as-metadata",
		resourceMeta: server.URL + "/resource-metadata", mcpURL: server.URL + "/mcp",
		expectCIMD: true, requestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunFailsWhenCIMDIsNotAdvertised(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": "https://issuer.example", "authorization_endpoint": "https://issuer.example/authorize",
			"token_endpoint": "https://issuer.example/token", "code_challenge_methods_supported": []string{"S256"},
		})
	}))
	defer server.Close()

	err := run(context.Background(), options{
		issuerURL: "https://issuer.example", authorizationMeta: server.URL,
		resourceMeta: server.URL, mcpURL: server.URL, expectCIMD: true, requestTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("missing CIMD advertisement was accepted")
	}
}
