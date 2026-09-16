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
	"sort"
	"strings"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

const gnasFleetRuntimeVersion = 1

type GNASFleetRuntimeManifest struct {
	Version  int                       `json:"version"`
	Bindings []GNASFleetRuntimeBinding `json:"bindings"`
}

type GNASFleetRuntimeBinding struct {
	BindingID          string `json:"binding_id"`
	InstanceConfigPath string `json:"instance_config_path"`
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
	baseURL := strings.TrimSpace(os.Getenv("GNAS_BASE_URL"))
	endpoint := gnasServiceURL(baseURL, "/gnas/service/resolveMCPBindingsV1")
	tokenEndpoint := gnasServiceURL(baseURL, "/gnas/service/getJwtToken")
	appID, appSecret := strings.TrimSpace(os.Getenv("GNAS_APP_ID")), os.Getenv("GNAS_APP_SECRET")
	if endpoint == "" || tokenEndpoint == "" || appID == "" || appSecret == "" {
		return nil, fmt.Errorf("GNAS MCP binding resolver configuration is incomplete")
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	provider := &GNASServiceJWTProvider{Endpoint: tokenEndpoint, Client: httpClient, AppID: appID, Secret: appSecret, now: time.Now}
	token, err := provider.Token(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := fetchGNASFleet(ctx, httpClient, endpoint, token)
	if err != nil {
		return nil, err
	}
	return mergeGNASFleet(payload, runtimeBindings, listenAddress)
}

func loadGNASFleetRuntimeManifest(path string) (map[string]string, error) {
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
	bindings := make(map[string]string, len(manifest.Bindings))
	paths := make(map[string]bool, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		if !fleetIdentifier.MatchString(binding.BindingID) || bindings[binding.BindingID] != "" || !filepath.IsAbs(binding.InstanceConfigPath) || paths[binding.InstanceConfigPath] {
			return nil, fmt.Errorf("GNAS fleet runtime binding is invalid or duplicated")
		}
		fileInfo, err := os.Lstat(binding.InstanceConfigPath)
		if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("GNAS fleet instance config must be a readable regular file and not a symlink")
		}
		bindings[binding.BindingID] = binding.InstanceConfigPath
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
	if payload.Version != 1 || len(payload.Bindings) == 0 || len(payload.Bindings) > 64 || len(payload.Digest) != 64 {
		return fmt.Errorf("GNAS MCP binding payload is invalid")
	}
	canonicalBindings := append([]gnasFleetBinding(nil), payload.Bindings...)
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

func mergeGNASFleet(payload gnasFleetPayload, runtimeBindings map[string]string, listenAddress string) ([]LoadedFleetBinding, error) {
	seenHosts := map[string]bool{}
	seenSources := map[string]bool{}
	seenStates := map[string]bool{}
	seenSchemas := map[string]bool{}
	loaded := make([]LoadedFleetBinding, 0, len(payload.Bindings))
	for _, remote := range payload.Bindings {
		instancePath := runtimeBindings[remote.BindingID]
		if instancePath == "" || !fleetIdentifier.MatchString(remote.BindingID) || !fleetIdentifier.MatchString(remote.AuthorizationResource) || remote.Plugins.Zoop == nil {
			return nil, fmt.Errorf("GNAS binding %q has no exact local runtime mapping", remote.BindingID)
		}
		parsed, err := url.Parse(remote.PublicResource)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
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
		if seenStates[runtime.StatePath] || runtime.SchemaMirrorPath != "" && seenSchemas[runtime.SchemaMirrorPath] {
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
		})
		if err != nil || !listenIsLoopback(cfg.ListenAddress) {
			return nil, fmt.Errorf("GNAS binding %s runtime configuration is invalid", remote.BindingID)
		}
		cfg.TrustedLoopbackProxy = true
		seenHosts[host] = true
		seenSources[remote.Source] = true
		seenStates[runtime.StatePath] = true
		if runtime.SchemaMirrorPath != "" {
			seenSchemas[runtime.SchemaMirrorPath] = true
		}
		delete(runtimeBindings, remote.BindingID)
		loaded = append(loaded, LoadedFleetBinding{Binding: binding, Config: cfg})
	}
	if len(runtimeBindings) != 0 {
		return nil, fmt.Errorf("local runtime manifest contains bindings absent from GNAS")
	}
	return loaded, nil
}
