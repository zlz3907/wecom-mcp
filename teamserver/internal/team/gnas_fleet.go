package team

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

const gnasFleetRuntimeVersion = 1

var fleetSecretEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type GNASFleetRuntimeManifest struct {
	Version  int                       `json:"version"`
	Bindings []GNASFleetRuntimeBinding `json:"bindings"`
}

type GNASFleetRuntimeBinding struct {
	BindingID          string `json:"binding_id"`
	InstanceConfigPath string `json:"instance_config_path"`
	ClientID           string `json:"client_id"`
	ClientSecretEnv    string `json:"client_secret_env"`
}

type gnasFleetZoopPlugin struct {
	RegistryDocumentID string `json:"registry_document_id"`
	RegistryKey        string `json:"registry_key"`
}

type gnasFleetPlugins struct {
	Zoop *gnasFleetZoopPlugin `json:"zoop,omitempty"`
}

type gnasFleetBinding struct {
	BindingID             string           `json:"binding_id"`
	PublicResource        string           `json:"public_resource"`
	AuthorizationResource string           `json:"authorization_resource"`
	Source                string           `json:"source"`
	Plugins               gnasFleetPlugins `json:"plugins"`
}

type gnasFleetPayload struct {
	Version  int                `json:"version"`
	Digest   string             `json:"digest"`
	Bindings []gnasFleetBinding `json:"bindings"`
}

type gnasFleetEnvelope struct {
	Code int              `json:"code"`
	Data gnasFleetPayload `json:"data"`
}

func LoadGNASFleet(ctx context.Context, runtimeManifestPath, listenAddress string) ([]LoadedFleetBinding, error) {
	runtimeBindings, err := loadGNASFleetRuntimeManifest(runtimeManifestPath)
	if err != nil {
		return nil, err
	}
	payload, err := resolveGNASFleet(ctx)
	if err != nil {
		return nil, err
	}
	if len(payload.Bindings) == 0 {
		return nil, fmt.Errorf("legacy GNAS runtime fleet requires bindings")
	}
	return mergeGNASFleet(payload, runtimeBindings, listenAddress)
}

func resolveGNASFleet(ctx context.Context) (gnasFleetPayload, error) {
	baseURL := strings.TrimSpace(os.Getenv("GNAS_BASE_URL"))
	endpoint := gnasServiceURL(baseURL, "/gnas/service/resolveMCPBindingsV1")
	tokenEndpoint := gnasServiceURL(baseURL, "/gnas/service/getJwtToken")
	appID, appSecret := strings.TrimSpace(os.Getenv("GNAS_APP_ID")), os.Getenv("GNAS_APP_SECRET")
	if endpoint == "" || tokenEndpoint == "" || appID == "" || appSecret == "" {
		return gnasFleetPayload{}, fmt.Errorf("GNAS MCP binding resolver configuration is incomplete")
	}
	httpClient := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider := &GNASServiceJWTProvider{Endpoint: tokenEndpoint, Client: httpClient, AppID: appID, Secret: appSecret, now: time.Now}
	token, err := provider.Token(ctx)
	if err != nil {
		return gnasFleetPayload{}, err
	}
	return fetchGNASFleet(ctx, httpClient, endpoint, token)
}

func loadGNASFleetRuntimeManifest(path string) (map[string]GNASFleetRuntimeBinding, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("--gnas-fleet-runtime must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("GNAS fleet runtime manifest must be a readable regular file and not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open GNAS fleet runtime manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest GNASFleetRuntimeManifest
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF || manifest.Version != gnasFleetRuntimeVersion || len(manifest.Bindings) == 0 || len(manifest.Bindings) > 64 {
		return nil, fmt.Errorf("GNAS fleet runtime manifest is invalid")
	}
	bindings := make(map[string]GNASFleetRuntimeBinding, len(manifest.Bindings))
	paths := make(map[string]bool, len(manifest.Bindings))
	clients := make(map[string]bool, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		if !fleetIdentifier.MatchString(binding.BindingID) || bindings[binding.BindingID].BindingID != "" || !filepath.IsAbs(binding.InstanceConfigPath) || paths[binding.InstanceConfigPath] {
			return nil, fmt.Errorf("GNAS fleet runtime binding is invalid or duplicated")
		}
		if binding.ClientID == "" || binding.ClientID != strings.TrimSpace(binding.ClientID) || clients[binding.ClientID] || !fleetSecretEnvironmentName.MatchString(binding.ClientSecretEnv) {
			return nil, fmt.Errorf("GNAS fleet runtime client credentials must have a unique client_id and explicit client_secret_env")
		}
		fileInfo, err := os.Lstat(binding.InstanceConfigPath)
		if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("GNAS fleet instance config must be a readable regular file and not a symlink")
		}
		bindings[binding.BindingID] = binding
		clients[binding.ClientID] = true
		paths[binding.InstanceConfigPath] = true
	}
	return bindings, nil
}

func fetchGNASFleet(ctx context.Context, client *http.Client, endpoint, token string) (gnasFleetPayload, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(nil))
	if err != nil {
		return gnasFleetPayload{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Auth-Type", "service_jwt")
	response, err := client.Do(request)
	if err != nil {
		return gnasFleetPayload{}, fmt.Errorf("resolve GNAS MCP bindings: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return gnasFleetPayload{}, fmt.Errorf("resolve GNAS MCP bindings returned HTTP %d", response.StatusCode)
	}
	if err := requireJSONContentType(response.Header.Get("Content-Type")); err != nil {
		return gnasFleetPayload{}, err
	}
	var envelope gnasFleetEnvelope
	if err := decodeStrictJSON(response.Body, &envelope); err != nil || envelope.Code != http.StatusOK {
		return gnasFleetPayload{}, fmt.Errorf("GNAS MCP binding response contract is invalid")
	}
	if err := validateGNASFleetPayload(envelope.Data); err != nil {
		return gnasFleetPayload{}, err
	}
	return envelope.Data, nil
}

func validateGNASFleetPayload(payload gnasFleetPayload) error {
	if payload.Version != 1 || payload.Bindings == nil || len(payload.Bindings) > 64 || len(payload.Digest) != 64 {
		return fmt.Errorf("GNAS MCP binding payload is invalid")
	}
	canonicalBindings := make([]gnasFleetBinding, len(payload.Bindings))
	copy(canonicalBindings, payload.Bindings)
	sort.Slice(canonicalBindings, func(i, j int) bool { return canonicalBindings[i].BindingID < canonicalBindings[j].BindingID })
	canonical, err := json.Marshal(struct {
		Version  int                `json:"version"`
		Bindings []gnasFleetBinding `json:"bindings"`
	}{Version: payload.Version, Bindings: canonicalBindings})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != payload.Digest {
		return fmt.Errorf("GNAS MCP binding digest mismatch")
	}
	return nil
}

func mergeGNASFleet(payload gnasFleetPayload, runtimeBindings map[string]GNASFleetRuntimeBinding, listenAddress string) ([]LoadedFleetBinding, error) {
	if strings.TrimSpace(os.Getenv("TEAM_MCP_AUTH_MODE")) != string(AuthenticationModeOAuth21) {
		return nil, fmt.Errorf("GNAS fleet requires oauth21 authentication for tenant and resource isolation")
	}
	seenHosts := map[string]bool{}
	seenSources := map[string]bool{}
	seenStates := map[string]bool{}
	seenSchemas := map[string]bool{}
	seenBindings := map[string]bool{}
	seenNames := map[string]bool{}
	loaded := make([]LoadedFleetBinding, 0, len(payload.Bindings))
	for _, remote := range payload.Bindings {
		local := runtimeBindings[remote.BindingID]
		instancePath := local.InstanceConfigPath
		if instancePath == "" || !fleetIdentifier.MatchString(remote.BindingID) || seenBindings[remote.BindingID] || !fleetIdentifier.MatchString(remote.AuthorizationResource) || remote.Plugins.Zoop == nil {
			return nil, fmt.Errorf("GNAS binding %q has no exact local runtime mapping", remote.BindingID)
		}
		parsed, err := url.Parse(remote.PublicResource)
		if err != nil || parsed.Scheme != "https" || !fleetHost.MatchString(parsed.Hostname()) || parsed.Port() != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("GNAS binding %s public resource is invalid", remote.BindingID)
		}
		host := strings.ToLower(parsed.Hostname())
		if seenHosts[host] || seenSources[remote.Source] {
			return nil, fmt.Errorf("GNAS binding %s reuses a host or source", remote.BindingID)
		}
		runtime, err := instanceconfig.LoadBootstrapCandidate(instancePath)
		if err != nil || runtime.TenantRoute != remote.Source || runtime.RegistryDocumentID != remote.Plugins.Zoop.RegistryDocumentID || runtime.RegistryKey != remote.Plugins.Zoop.RegistryKey {
			return nil, fmt.Errorf("GNAS binding %s does not match its protected local runtime", remote.BindingID)
		}
		if seenNames[runtime.InstanceName] || seenStates[filepath.Clean(runtime.StatePath)] || runtime.SchemaMirrorPath != "" && seenSchemas[filepath.Clean(runtime.SchemaMirrorPath)] {
			return nil, fmt.Errorf("GNAS binding %s reuses local state", remote.BindingID)
		}
		binding := FleetBinding{
			BindingID: remote.BindingID, Hosts: []string{host}, PublicURL: strings.TrimSuffix(remote.PublicResource, "/"),
			AuthorizationTenant: remote.BindingID, AuthorizationResource: remote.AuthorizationResource,
			Source: remote.Source, InstanceConfigPath: instancePath, Plugins: []string{"zoop"},
		}
		oauthIssuer := parsed.Scheme + "://" + parsed.Host + "/gnas/oauth"
		cfg, err := LoadConfigForBinding(instancePath, listenAddress, BindingOverrides{
			PublicURL: binding.PublicURL, OIDCIssuer: oauthIssuer, AuthorizationTenant: binding.AuthorizationTenant,
			AuthorizationResource: binding.AuthorizationResource, Plugins: binding.Plugins,
			OAuth21Credentials: &OAuth21ClientCredentials{ClientID: local.ClientID, ClientSecret: os.Getenv(local.ClientSecretEnv)},
		})
		if err != nil || cfg.AuthenticationMode != AuthenticationModeOAuth21 || !listenIsLoopback(cfg.ListenAddress) {
			return nil, fmt.Errorf("GNAS binding %s runtime configuration is invalid", remote.BindingID)
		}
		cfg.TrustedLoopbackProxy = true
		seenHosts[host] = true
		seenSources[remote.Source] = true
		seenBindings[remote.BindingID] = true
		seenNames[runtime.InstanceName] = true
		seenStates[filepath.Clean(runtime.StatePath)] = true
		if runtime.SchemaMirrorPath != "" {
			seenSchemas[filepath.Clean(runtime.SchemaMirrorPath)] = true
		}
		loaded = append(loaded, LoadedFleetBinding{Binding: binding, Config: cfg})
	}
	// Local entries are runtime dependencies, not authority to enable a route.
	// A removed remote binding must disappear even while its local assets remain.
	return loaded, nil
}
