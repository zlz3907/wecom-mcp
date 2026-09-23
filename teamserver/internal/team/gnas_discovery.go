package team

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

var bindingDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func gnasBindingDigest(b gnasFleetBinding) string {
	canonical, _ := json.Marshal(b)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

// DiscoveryPolicy contains only common local capability restrictions. Tenant
// identity, hosts and Registry coordinates come exclusively from GNAS.
type DiscoveryPolicy struct {
	Version      int                 `json:"version"`
	APIWhitelist map[string][]string `json:"api_whitelist"`
}

type discoveredInstance struct {
	binding gnasFleetBinding
	name    string
}

type GNASDiscovery struct {
	runtimeManifestPath string
	policyPath          string
	stateRoot           string
	listen              string
	mu                  sync.Mutex
	instances           map[string]discoveredInstance
	resolveName         func(context.Context, instanceconfig.Config) (string, error)
}

func NewGNASDiscovery(policyPath, stateRoot, listen string) (*GNASDiscovery, error) {
	if os.Getenv("GNAS_WECOM_TRANSPORT") != "managed_executor" {
		return nil, fmt.Errorf("database discovery requires the managed GNAS Source executor")
	}
	listen = firstNonEmpty(listen, os.Getenv("TEAM_MCP_LISTEN_ADDR"), "127.0.0.1:17801")
	if err := validateListenAddress(listen); err != nil || !listenIsLoopback(listen) {
		return nil, fmt.Errorf("discovery listener must be loopback")
	}
	if !filepath.IsAbs(policyPath) || !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || stateRoot == string(filepath.Separator) {
		return nil, fmt.Errorf("discovery policy and state root must be absolute; state root must be canonical and dedicated")
	}
	// Require a pre-provisioned root. No configuration or business asset is
	// created by startup/check-config, and aliases cannot mix tenant state.
	resolved, err := filepath.EvalSymlinks(stateRoot)
	info, statErr := os.Stat(stateRoot)
	if err != nil || statErr != nil || !info.IsDir() || resolved != stateRoot {
		return nil, fmt.Errorf("discovery state root must be an existing directory without symlink aliases")
	}
	return &GNASDiscovery{policyPath: policyPath, stateRoot: stateRoot, listen: listen, instances: map[string]discoveredInstance{}, resolveName: legacymcp.ResolveDiscoveredInstanceName}, nil
}

// NewGNASHybridDiscovery keeps explicitly mapped local instances at full
// capability while discovering unmapped bindings as reader-only instances.
func NewGNASHybridDiscovery(policyPath, stateRoot, runtimeManifestPath, listen string) (*GNASDiscovery, error) {
	d, err := NewGNASDiscovery(policyPath, stateRoot, listen)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(runtimeManifestPath) {
		return nil, fmt.Errorf("hybrid runtime manifest must be absolute")
	}
	d.runtimeManifestPath = runtimeManifestPath
	return d, nil
}

func (d *GNASDiscovery) ListenAddress() string { return d.listen }

func (d *GNASDiscovery) Load(ctx context.Context) ([]LoadedFleetBinding, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	policy, err := loadDiscoveryPolicy(d.policyPath)
	if err != nil {
		return nil, err
	}
	payload, err := resolveGNASFleet(ctx)
	if err != nil {
		return nil, err
	}
	var locals map[string]GNASFleetRuntimeBinding
	if d.runtimeManifestPath != "" {
		locals, err = loadGNASFleetRuntimeManifest(d.runtimeManifestPath)
		if err != nil {
			return nil, err
		}
	}
	return d.assembleWithLocals(ctx, payload, policy, locals)
}

func (d *GNASDiscovery) assemble(ctx context.Context, payload gnasFleetPayload, policy DiscoveryPolicy) ([]LoadedFleetBinding, error) {
	return d.assembleWithLocals(ctx, payload, policy, nil)
}

func (d *GNASDiscovery) assembleWithLocals(ctx context.Context, payload gnasFleetPayload, policy DiscoveryPolicy, locals map[string]GNASFleetRuntimeBinding) ([]LoadedFleetBinding, error) {
	if err := validateGNASFleetPayload(payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(os.Getenv("TEAM_MCP_AUTH_MODE")) != string(AuthenticationModeOAuth21) {
		return nil, fmt.Errorf("GNAS discovery requires oauth21 authentication")
	}
	seenIDs, seenHosts, seenSources := map[string]bool{}, map[string]bool{}, map[string]bool{}
	// Validate the entire authority snapshot before reading any tenant data.
	for _, b := range payload.Bindings {
		u, err := url.Parse(b.PublicResource)
		if err != nil || u.Scheme != "https" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !fleetHost.MatchString(u.Hostname()) || !fleetIdentifier.MatchString(b.BindingID) || !fleetIdentifier.MatchString(b.AuthorizationResource) || seenIDs[b.BindingID] || seenHosts[u.Hostname()] || seenSources[b.Source] || b.Plugins.Zoop == nil {
			return nil, fmt.Errorf("GNAS discovery binding contract is invalid")
		}
		if _, err := normalizeTeamPublicURL(b.PublicResource); err != nil {
			return nil, fmt.Errorf("GNAS discovery public resource is invalid")
		}
		// Reuse the fixed instance validator for Source, Registry and policy.
		runtime := d.runtimeFor(b, policy)
		if err := runtime.Validate(); err != nil {
			return nil, fmt.Errorf("GNAS discovery instance contract is invalid")
		}
		seenIDs[b.BindingID], seenHosts[u.Hostname()], seenSources[b.Source] = true, true, true
	}
	instances := make(map[string]discoveredInstance, len(payload.Bindings))
	loaded := make([]LoadedFleetBinding, 0, len(payload.Bindings))
	for _, b := range payload.Bindings {
		if local, ok := locals[b.BindingID]; ok {
			binding, err := d.localBinding(b, local)
			if err != nil {
				return nil, err
			}
			loaded = append(loaded, binding)
			continue
		}
		runtime := d.runtimeFor(b, policy)
		cached, ok := d.instances[b.BindingID]
		if !ok || !reflect.DeepEqual(cached.binding, b) {
			name, err := d.resolveName(ctx, runtime)
			if err != nil {
				return nil, fmt.Errorf("GNAS discovery Registry validation failed")
			}
			cached = discoveredInstance{binding: b, name: name}
		}
		runtime.InstanceName = cached.name
		u, _ := url.Parse(b.PublicResource)
		cfg, err := LoadConfigForBinding("", d.listen, BindingOverrides{
			Runtime: &runtime, OAuth21ServiceJWT: true,
			GNASBindingDigest: gnasBindingDigest(b),
			PublicURL:         b.PublicResource, OIDCIssuer: "https://" + u.Host + "/gnas/oauth",
			AuthorizationTenant: b.BindingID, AuthorizationResource: b.AuthorizationResource, Plugins: []string{"zoop"},
		})
		if err != nil {
			return nil, fmt.Errorf("GNAS discovery runtime configuration is invalid")
		}
		if err := CheckRuntimeSource(cfg); err != nil {
			return nil, fmt.Errorf("GNAS discovery Source configuration is invalid")
		}
		cfg.TrustedLoopbackProxy = true
		instances[b.BindingID] = cached
		loaded = append(loaded, LoadedFleetBinding{Binding: FleetBinding{
			BindingID: b.BindingID, Hosts: []string{u.Hostname()}, PublicURL: b.PublicResource,
			AuthorizationTenant: b.BindingID, AuthorizationResource: b.AuthorizationResource,
			Source: b.Source, Plugins: []string{"zoop"},
		}, Config: cfg})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateHybridStorage(loaded); err != nil {
		return nil, err
	}
	d.instances = instances
	return loaded, nil
}

func (d *GNASDiscovery) runtimeFor(b gnasFleetBinding, policy DiscoveryPolicy) instanceconfig.Config {
	// Reusing a binding ID for a different Source/Registry must not reuse its
	// identity journals. A flat digest filename avoids remote path traversal.
	identity, _ := json.Marshal(b)
	digest := sha256.Sum256(identity)
	groups := make(map[string][]string, len(policy.APIWhitelist))
	for name, operations := range policy.APIWhitelist {
		groups[name] = append([]string(nil), operations...)
	}
	return instanceconfig.Config{Version: 1, InstanceName: b.BindingID, TenantRoute: b.Source,
		RegistryDocumentID: b.Plugins.Zoop.RegistryDocumentID, RegistryKey: b.Plugins.Zoop.RegistryKey,
		SchemaSource: "z-s00", StatePath: filepath.Join(d.stateRoot, hex.EncodeToString(digest[:])+".json"), APIWhitelist: groups}
}

func loadDiscoveryPolicy(path string) (DiscoveryPolicy, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return DiscoveryPolicy{}, fmt.Errorf("discovery policy must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return DiscoveryPolicy{}, fmt.Errorf("discovery policy unavailable")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	decoder.DisallowUnknownFields()
	var policy DiscoveryPolicy
	if decoder.Decode(&policy) != nil || decoder.Decode(&struct{}{}) != io.EOF || policy.Version != 1 || len(policy.APIWhitelist) == 0 {
		return DiscoveryPolicy{}, fmt.Errorf("discovery policy is invalid")
	}
	// Validate even while the authoritative tenant set is empty, so a bad
	// common policy cannot be accepted only to break the first later tenant.
	validation := instanceconfig.Config{Version: 1, InstanceName: "policy-validation", TenantRoute: "policy-validation", RegistryDocumentID: "policy-validation", RegistryKey: "policy-validation", StatePath: filepath.Join(os.TempDir(), "discovery-policy-validation.json"), APIWhitelist: policy.APIWhitelist}
	if err := validation.Validate(); err != nil || !validation.Allows("get_sheet") || !validation.Allows("get_fields") || !validation.Allows("get_records") {
		return DiscoveryPolicy{}, fmt.Errorf("discovery policy must permit Registry reads and contain only supported operations")
	}
	return policy, nil
}

func (d *GNASDiscovery) localBinding(b gnasFleetBinding, local GNASFleetRuntimeBinding) (LoadedFleetBinding, error) {
	runtime, err := instanceconfig.Load(local.InstanceConfigPath)
	if err != nil || runtime.TenantRoute != b.Source || runtime.RegistryDocumentID != b.Plugins.Zoop.RegistryDocumentID || runtime.RegistryKey != b.Plugins.Zoop.RegistryKey {
		return LoadedFleetBinding{}, fmt.Errorf("protected local instance does not match the authoritative Binding")
	}
	u, _ := url.Parse(b.PublicResource)
	cfg, err := LoadConfigForBinding(local.InstanceConfigPath, d.listen, BindingOverrides{
		BoundRuntime: &runtime, OAuth21ServiceJWT: true, GNASBindingDigest: gnasBindingDigest(b),
		PublicURL: b.PublicResource, OIDCIssuer: "https://" + u.Host + "/gnas/oauth", AuthorizationTenant: b.BindingID, AuthorizationResource: b.AuthorizationResource, Plugins: []string{"zoop"},
	})
	if err != nil {
		return LoadedFleetBinding{}, fmt.Errorf("hybrid local runtime configuration invalid")
	}
	if err := CheckRuntimeSource(cfg); err != nil {
		return LoadedFleetBinding{}, fmt.Errorf("hybrid local Source configuration invalid")
	}
	cfg.TrustedLoopbackProxy = true
	return LoadedFleetBinding{Binding: FleetBinding{BindingID: b.BindingID, Hosts: []string{u.Hostname()}, PublicURL: b.PublicResource, AuthorizationTenant: b.BindingID, AuthorizationResource: b.AuthorizationResource, Source: b.Source, InstanceConfigPath: local.InstanceConfigPath, Plugins: []string{"zoop"}}, Config: cfg}, nil
}

// Config, state and schema paths cannot alias any other tenant's artifacts.
func validateHybridStorage(bindings []LoadedFleetBinding) error {
	names, paths := map[string]bool{}, map[string]bool{}
	for _, b := range bindings {
		runtime := b.Config.Runtime
		if b.Config.BoundRuntime != nil {
			runtime = b.Config.BoundRuntime
		}
		if runtime == nil || names[runtime.InstanceName] {
			return fmt.Errorf("hybrid instance name duplicated or missing")
		}
		names[runtime.InstanceName] = true
		for _, p := range []string{b.Config.InstanceConfigPath, runtime.StatePath, runtime.SchemaMirrorPath} {
			if p == "" {
				continue
			}
			if !filepath.IsAbs(p) || filepath.Clean(p) != p || paths[p] {
				return fmt.Errorf("hybrid instance storage aliases another artifact")
			}
			resolved, err := filepath.EvalSymlinks(p)
			if os.IsNotExist(err) {
				parent, parentErr := filepath.EvalSymlinks(filepath.Dir(p))
				if parentErr != nil {
					return fmt.Errorf("hybrid storage parent must exist")
				}
				resolved, err = filepath.Join(parent, filepath.Base(p)), nil
			}
			if err != nil || resolved != p {
				return fmt.Errorf("hybrid storage must not use symlink aliases")
			}
			paths[p] = true
		}
	}
	return nil
}
