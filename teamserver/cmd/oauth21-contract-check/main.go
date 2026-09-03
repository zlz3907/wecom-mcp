package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const responseLimit = 64 << 10

type options struct {
	issuerURL         string
	authorizationMeta string
	resourceMeta      string
	mcpURL            string
	tokenFile         string
	expectCIMD        bool
	requestTimeout    time.Duration
}

type authorizationMetadata struct {
	Issuer                          string   `json:"issuer"`
	AuthorizationEndpoint           string   `json:"authorization_endpoint"`
	TokenEndpoint                   string   `json:"token_endpoint"`
	CodeChallengeMethodsSupported   []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataDocumentSupport bool     `json:"client_id_metadata_document_supported"`
}

type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func main() {
	var opts options
	flag.StringVar(&opts.issuerURL, "issuer", "", "expected OAuth issuer URL")
	flag.StringVar(&opts.authorizationMeta, "authorization-metadata", "", "authorization server metadata URL")
	flag.StringVar(&opts.resourceMeta, "resource-metadata", "", "protected resource metadata URL")
	flag.StringVar(&opts.mcpURL, "mcp-url", "", "Streamable HTTP MCP URL")
	flag.StringVar(&opts.tokenFile, "token-file", "", "optional 0600 file containing a short-lived access token")
	flag.BoolVar(&opts.expectCIMD, "expect-cimd", true, "require client_id_metadata_document_supported=true")
	flag.DurationVar(&opts.requestTimeout, "timeout", 10*time.Second, "overall check timeout")
	flag.Parse()

	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, "oauth21 contract check failed:", err)
		os.Exit(1)
	}
}

func run(parent context.Context, opts options) error {
	if opts.issuerURL == "" || opts.authorizationMeta == "" || opts.resourceMeta == "" || opts.mcpURL == "" {
		return errors.New("issuer, authorization-metadata, resource-metadata, and mcp-url are required")
	}
	if opts.requestTimeout <= 0 || opts.requestTimeout > time.Minute {
		return errors.New("timeout must be greater than zero and at most one minute")
	}
	ctx, cancel := context.WithTimeout(parent, opts.requestTimeout)
	defer cancel()
	httpClient := &http.Client{
		Timeout: opts.requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	var authorization authorizationMetadata
	if err := getJSON(ctx, httpClient, opts.authorizationMeta, &authorization); err != nil {
		return fmt.Errorf("authorization metadata: %w", err)
	}
	if authorization.Issuer != opts.issuerURL || authorization.AuthorizationEndpoint == "" || authorization.TokenEndpoint == "" || !slices.Contains(authorization.CodeChallengeMethodsSupported, "S256") {
		return errors.New("authorization metadata does not match issuer/endpoints/PKCE S256 contract")
	}
	if opts.expectCIMD && !authorization.ClientIDMetadataDocumentSupport {
		return errors.New("authorization metadata does not advertise CIMD")
	}

	var resource protectedResourceMetadata
	if err := getJSON(ctx, httpClient, opts.resourceMeta, &resource); err != nil {
		return fmt.Errorf("protected resource metadata: %w", err)
	}
	if resource.Resource != opts.mcpURL || !slices.Contains(resource.AuthorizationServers, opts.issuerURL) {
		return errors.New("protected resource metadata does not match MCP URL/issuer contract")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.mcpURL, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("unauthenticated MCP probe: %w", err)
	}
	io.Copy(io.Discard, io.LimitReader(response.Body, responseLimit))
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(response.Header.Get("WWW-Authenticate"), `resource_metadata="`+opts.resourceMeta+`"`) {
		return fmt.Errorf("unauthenticated MCP probe returned status=%d without exact resource metadata challenge", response.StatusCode)
	}

	toolCount := 0
	if opts.tokenFile != "" {
		token, err := readProtectedToken(opts.tokenFile)
		if err != nil {
			return err
		}
		authenticatedClient := &http.Client{
			Timeout:       opts.requestTimeout,
			Transport:     bearerTransport{token: token, base: http.DefaultTransport},
			CheckRedirect: httpClient.CheckRedirect,
		}
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "oauth21-contract-check", Version: "1"}, nil)
		session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
			Endpoint: opts.mcpURL, HTTPClient: authenticatedClient, DisableStandaloneSSE: true, MaxRetries: -1,
		}, nil)
		if err != nil {
			return fmt.Errorf("authenticated MCP initialize: %w", err)
		}
		defer session.Close()
		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			return fmt.Errorf("authenticated MCP tools/list: %w", err)
		}
		toolCount = len(tools.Tools)
		if toolCount == 0 {
			return errors.New("authenticated MCP tools/list returned no authorized tools")
		}
	}

	result := map[string]any{
		"authorization_metadata":      "ok",
		"protected_resource_metadata": "ok",
		"unauthenticated_challenge":   "ok",
		"authenticated_mcp":           opts.tokenFile != "",
		"authorized_tool_count":       toolCount,
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("response content type is not application/json")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(body) > responseLimit {
		return errors.New("response is unreadable or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("response contains trailing JSON")
	}
	return nil
}

func readProtectedToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("token file must be a regular file without group/other permissions")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if len(body) > 16<<10 {
		return "", errors.New("token file is too large")
	}
	token := strings.TrimSpace(string(body))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("token file does not contain one valid opaque token")
	}
	return token, nil
}
