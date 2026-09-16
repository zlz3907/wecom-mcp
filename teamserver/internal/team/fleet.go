package team

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

const fleetManifestVersion = 1

var fleetIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var fleetHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

type FleetManifest struct {
	Version  int            `json:"version"`
	Bindings []FleetBinding `json:"bindings"`
}

// FleetBinding contains only non-secret routing and instance references.
// Enterprise WeCom credentials remain behind the exact GNAS source named by
// Source; callers never select that source in MCP tool arguments.
type FleetBinding struct {
	BindingID             string   `json:"binding_id"`
	Hosts                 []string `json:"hosts"`
	PublicURL             string   `json:"public_url"`
	AuthorizationTenant   string   `json:"authorization_tenant"`
	AuthorizationResource string   `json:"authorization_resource"`
	Source                string   `json:"source"`
	InstanceConfigPath    string   `json:"instance_config_path"`
	Plugins               []string `json:"plugins"`
}

type LoadedFleetBinding struct {
	Binding FleetBinding
	Config  Config
}

func LoadFleetManifest(path, listenAddress string) ([]LoadedFleetBinding, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("--fleet must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("fleet manifest must be a readable regular file and not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fleet manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest FleetManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode fleet manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("fleet manifest must contain one JSON object")
	}
	if manifest.Version != fleetManifestVersion || len(manifest.Bindings) == 0 {
		return nil, fmt.Errorf("fleet manifest version or bindings are invalid")
	}
	seenBindings := map[string]bool{}
	seenHosts := map[string]bool{}
	seenSources := map[string]bool{}
	seenConfigs := map[string]bool{}
	seenStates := map[string]bool{}
	seenSchemas := map[string]bool{}
	seenInstances := map[string]bool{}
	loaded := make([]LoadedFleetBinding, 0, len(manifest.Bindings))
	for index, binding := range manifest.Bindings {
		if err := validateFleetBinding(binding, seenBindings, seenHosts, seenSources, seenConfigs); err != nil {
			return nil, fmt.Errorf("fleet binding %d: %w", index, err)
		}
		runtime, err := instanceconfig.LoadBootstrapCandidate(binding.InstanceConfigPath)
		if err != nil {
			return nil, fmt.Errorf("fleet binding %s instance config: %w", binding.BindingID, err)
		}
		if runtime.TenantRoute != binding.Source {
			return nil, fmt.Errorf("fleet binding %s source does not match the fixed instance tenant_route", binding.BindingID)
		}
		if seenInstances[runtime.InstanceName] {
			return nil, fmt.Errorf("fleet binding %s reuses another instance_name", binding.BindingID)
		}
		seenInstances[runtime.InstanceName] = true
		if seenStates[runtime.StatePath] {
			return nil, fmt.Errorf("fleet binding %s reuses another instance state_path", binding.BindingID)
		}
		seenStates[runtime.StatePath] = true
		if runtime.SchemaMirrorPath != "" {
			if seenSchemas[runtime.SchemaMirrorPath] {
				return nil, fmt.Errorf("fleet binding %s reuses another schema_mirror_path", binding.BindingID)
			}
			seenSchemas[runtime.SchemaMirrorPath] = true
		}
		cfg, err := LoadConfigForBinding(binding.InstanceConfigPath, listenAddress, BindingOverrides{
			PublicURL: binding.PublicURL, AuthorizationTenant: binding.AuthorizationTenant,
			AuthorizationResource: binding.AuthorizationResource, Plugins: binding.Plugins,
		})
		if err != nil {
			return nil, fmt.Errorf("fleet binding %s team config: %w", binding.BindingID, err)
		}
		if cfg.AuthenticationMode != AuthenticationModeOAuth21 {
			return nil, fmt.Errorf("fleet binding %s requires oauth21 authentication for tenant and resource isolation", binding.BindingID)
		}
		if !listenIsLoopback(cfg.ListenAddress) {
			return nil, fmt.Errorf("fleet binding %s must listen on a loopback address", binding.BindingID)
		}
		// The fleet HostRouter is the DNS-rebinding boundary for this loopback
		// listener. It accepts only exact manifest hosts before the request
		// reaches the SDK handler.
		cfg.TrustedLoopbackProxy = true
		loaded = append(loaded, LoadedFleetBinding{Binding: binding, Config: cfg})
	}
	return loaded, nil
}

func validateFleetBinding(binding FleetBinding, seenBindings, seenHosts, seenSources, seenConfigs map[string]bool) error {
	if !fleetIdentifier.MatchString(binding.BindingID) || seenBindings[binding.BindingID] {
		return fmt.Errorf("binding_id is invalid or duplicated")
	}
	seenBindings[binding.BindingID] = true
	if binding.Source == "" || binding.Source != strings.TrimSpace(binding.Source) || seenSources[binding.Source] {
		return fmt.Errorf("source is invalid or duplicated")
	}
	seenSources[binding.Source] = true
	if !filepath.IsAbs(binding.InstanceConfigPath) || seenConfigs[binding.InstanceConfigPath] {
		return fmt.Errorf("instance_config_path must be absolute and unique")
	}
	info, err := os.Lstat(binding.InstanceConfigPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("instance_config_path must be a readable regular file and not a symlink")
	}
	seenConfigs[binding.InstanceConfigPath] = true
	if len(binding.Hosts) == 0 {
		return fmt.Errorf("hosts must not be empty")
	}
	parsed, err := url.Parse(binding.PublicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return fmt.Errorf("public_url must be an absolute HTTPS URL")
	}
	publicHost := strings.ToLower(parsed.Hostname())
	publicHostPresent := false
	localHosts := map[string]bool{}
	for _, rawHost := range binding.Hosts {
		host := strings.ToLower(rawHost)
		if rawHost != host || !fleetHost.MatchString(host) || localHosts[host] || seenHosts[host] {
			return fmt.Errorf("host %q is invalid or duplicated", rawHost)
		}
		localHosts[host] = true
		seenHosts[host] = true
		publicHostPresent = publicHostPresent || host == publicHost
	}
	if !publicHostPresent {
		return fmt.Errorf("public_url host must be listed in hosts")
	}
	if len(binding.Plugins) == 0 {
		return fmt.Errorf("plugins must be explicit")
	}
	if _, err := legacymcp.ToolDefinitionsForPlugins(binding.Plugins, false); err != nil {
		return err
	}
	return nil
}

type HostRouter struct {
	handlers map[string]http.Handler
}

func NewHostRouter(bindings []LoadedFleetBinding, handlers map[string]http.Handler) (*HostRouter, error) {
	routes := map[string]http.Handler{}
	for _, binding := range bindings {
		handler := handlers[binding.Binding.BindingID]
		if handler == nil {
			return nil, fmt.Errorf("fleet binding %s has no handler", binding.Binding.BindingID)
		}
		for _, host := range binding.Binding.Hosts {
			if routes[host] != nil {
				return nil, fmt.Errorf("fleet host %s is duplicated", host)
			}
			routes[host] = handler
		}
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("fleet router has no hosts")
	}
	return &HostRouter{handlers: routes}, nil
}

func (r *HostRouter) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	host := strings.ToLower(request.Host)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	if !fleetHost.MatchString(host) {
		http.Error(w, "unknown MCP host", http.StatusMisdirectedRequest)
		return
	}
	handler := r.handlers[host]
	if handler == nil {
		http.Error(w, "unknown MCP host", http.StatusMisdirectedRequest)
		return
	}
	handler.ServeHTTP(w, request)
}
