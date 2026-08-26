// wecom-mcp-v2-configure performs the small, deliberately constrained client
// registration step used by the portable installer.  It never reads or writes
// Enterprise WeCom data and it refuses to guess an unknown client's format.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	instanceName = "zoop_wecom_zhycit"
	beginMarker  = "# BEGIN wecom-mcp-v2 managed zoop_wecom_zhycit"
	endMarker    = "# END wecom-mcp-v2 managed zoop_wecom_zhycit"
)

type result struct {
	Client     string `json:"client"`
	Configured bool   `json:"configured"`
	ConfigPath string `json:"config_path,omitempty"`
	BackupPath string `json:"backup_path,omitempty"`
	Result     string `json:"result"`
	NextAction string `json:"next_action,omitempty"`
}

type options struct {
	client          string
	binary          string
	serviceConfig   string
	codexConfig     string
	traeConfig      string
	workBuddyConfig string
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(result{Client: "unknown", Result: "agent_blocked", NextAction: "set HOME and retry"}, 3)
	}
	opt := options{}
	var switchPrefix, switchRelease string
	defaultTRAEConfig := defaultTRAEConfigPath(home, os.Getenv("APPDATA"), runtime.GOOS)
	defaultServiceConfig := defaultServiceConfigPath(home, os.Getenv("LOCALAPPDATA"), os.Getenv("XDG_CONFIG_HOME"), runtime.GOOS)
	flag.StringVar(&opt.client, "client", "auto", "auto|codex|trae-solo-cn|trae-work-cn|workbuddy|none")
	flag.StringVar(&opt.binary, "binary", "", "absolute MCP binary path")
	flag.StringVar(&opt.serviceConfig, "config", defaultServiceConfig, "absolute instance configuration path; defaults to the managed per-user location")
	flag.StringVar(&opt.codexConfig, "codex-config", filepath.Join(home, ".codex", "config.toml"), "Codex config.toml path")
	flag.StringVar(&opt.traeConfig, "trae-config", defaultTRAEConfig, "TRAE mcp.json path")
	flag.StringVar(&opt.workBuddyConfig, "workbuddy-config", "", "reserved: WorkBuddy config path")
	flag.StringVar(&switchPrefix, "switch-current", "", "internal: atomically switch a managed prefix current link")
	flag.StringVar(&switchRelease, "release", "", "internal: managed release name for --switch-current")
	flag.Parse()
	if switchPrefix != "" || switchRelease != "" {
		if err := atomicSwitchCurrent(switchPrefix, switchRelease); err != nil {
			fatal(result{Client: "installer", Result: "agent_blocked", NextAction: err.Error()}, 3)
		}
		printResults([]result{{Client: "installer", Configured: false, Result: "switched"}})
		return
	}

	if !validClient(opt.client) {
		fatal(result{Client: opt.client, Result: "agent_blocked", NextAction: "use --client auto, codex, trae-solo-cn, trae-work-cn, workbuddy, or none"}, 3)
	}
	if opt.client == "none" {
		printResults([]result{{Client: "none", Result: "skipped", NextAction: "restart a client only after registering it explicitly"}})
		return
	}
	if err := validateInput(opt); err != nil {
		fatal(result{Client: opt.client, Result: "agent_blocked", NextAction: err.Error()}, 3)
	}

	var results []result
	switch opt.client {
	case "codex":
		results = append(results, configureCodex(opt))
	case "trae-solo-cn":
		results = append(results, configureTRAE(opt))
	case "trae-work-cn":
		results = append(results, configureTRAE(opt))
	case "workbuddy":
		results = append(results, workBuddyBlocked(opt))
	case "auto":
		if fileExists(opt.codexConfig) {
			results = append(results, configureCodex(opt))
		}
		if fileExists(opt.traeConfig) {
			results = append(results, configureTRAE(opt))
		}
		if len(results) == 0 {
			results = append(results, result{Client: "auto", Result: "agent_blocked", NextAction: "no known Codex or TRAE SOLO CN configuration found; rerun with --client codex or --client trae-solo-cn after confirming the client is installed"})
		}
	}
	printResults(results)
	for _, item := range results {
		if item.Result == "agent_blocked" {
			os.Exit(3)
		}
	}
}

func defaultTRAEConfigPath(home, appData, goos string) string {
	if goos == "windows" && strings.TrimSpace(appData) != "" {
		return filepath.Join(appData, "TRAE SOLO CN", "User", "mcp.json")
	}
	return filepath.Join(home, "Library", "Application Support", "TRAE SOLO CN", "User", "mcp.json")
}

func defaultServiceConfigPath(home, localAppData, xdgConfigHome, goos string) string {
	if goos == "windows" && strings.TrimSpace(localAppData) != "" {
		return filepath.Join(localAppData, "wecom-mcp-v2", "config", "zoop_wecom_zhycit.local.json")
	}
	if strings.TrimSpace(xdgConfigHome) != "" && filepath.IsAbs(xdgConfigHome) {
		return filepath.Join(xdgConfigHome, "wecom-mcp-v2", "zoop_wecom_zhycit.local.json")
	}
	return filepath.Join(home, ".config", "wecom-mcp-v2", "zoop_wecom_zhycit.local.json")
}

func atomicSwitchCurrent(prefix, release string) error {
	if !filepath.IsAbs(prefix) || release == "" || strings.ContainsAny(release, "/\\") {
		return errors.New("--switch-current requires an absolute prefix and a simple release name")
	}
	target := filepath.Join(prefix, "releases", release)
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return errors.New("requested managed release does not exist")
	}
	current := filepath.Join(prefix, "current")
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("current exists but is not a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := filepath.Join(prefix, fmt.Sprintf(".current-%d", os.Getpid()))
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(filepath.ToSlash(filepath.Join("releases", release)), temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, current); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func validClient(value string) bool {
	return value == "auto" || value == "codex" || value == "trae-solo-cn" || value == "trae-work-cn" || value == "workbuddy" || value == "none"
}

func validateInput(opt options) error {
	if opt.binary == "" || !filepath.IsAbs(opt.binary) {
		return errors.New("--binary must be an existing absolute path")
	}
	if info, err := os.Lstat(opt.binary); err != nil {
		return fmt.Errorf("--binary is not readable: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) || (runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(opt.binary), ".exe")) {
		return errors.New("--binary must be a regular executable file, not a symlink or special file")
	}
	if err := validateConfigTarget(opt.serviceConfig, "--config", false); err != nil {
		return err
	}
	return nil
}

func configureCodex(opt options) result {
	path := opt.codexConfig
	if err := validateConfigTarget(path, "--codex-config", true); err != nil {
		return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	block, err := codexBlock(opt.binary, opt.serviceConfig)
	if err != nil {
		return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeNew(path, []byte(block)); err != nil {
			return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
		}
		return result{Client: "codex", Configured: true, ConfigPath: path, Result: "configured", NextAction: "restart Codex, then run initialize, tools/list, and a read-only wecom_schema_status call"}
	}
	if err != nil {
		return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	text := string(data)
	if strings.Contains(text, beginMarker) || strings.Contains(text, endMarker) {
		if strings.Contains(text, beginMarker) && strings.Contains(text, endMarker) && strings.Contains(text, block) {
			return result{Client: "codex", Configured: true, ConfigPath: path, Result: "already_configured", NextAction: "restart Codex if it is running"}
		}
		return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: "managed Codex block is incomplete or conflicts with requested paths; inspect it manually"}
	}
	if strings.Contains(text, "[mcp_servers."+instanceName+"]") {
		return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: "an unmanaged zoop_wecom_zhycit Codex entry already exists; inspect and migrate it manually"}
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	backup, err := backupAndReplace(path, []byte(text+"\n"+block))
	if err != nil {
		return result{Client: "codex", ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	return result{Client: "codex", Configured: true, ConfigPath: path, BackupPath: backup, Result: "configured", NextAction: "restart Codex, then run initialize, tools/list, and a read-only wecom_schema_status call"}
}

func codexBlock(binary, serviceConfig string) (string, error) {
	if strings.ContainsAny(binary+serviceConfig, "\\\"\n\r") {
		return "", errors.New("paths containing backslashes, quotes, or newlines are not supported for TOML registration")
	}
	return fmt.Sprintf("%s\n[mcp_servers.%s]\ncommand = \"%s\"\nargs = [\"--config\", \"%s\"]\nenabled = true\n%s\n", beginMarker, instanceName, binary, serviceConfig, endMarker), nil
}

func configureTRAE(opt options) result {
	clientName := opt.client
	if clientName == "" || clientName == "auto" {
		clientName = "trae-solo-cn"
	}
	path := opt.traeConfig
	if err := validateConfigTarget(path, "--trae-config", true); err != nil {
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	var root map[string]any
	data, err := os.ReadFile(path)
	newFile := errors.Is(err, os.ErrNotExist)
	if newFile {
		root = map[string]any{}
	} else if err != nil {
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	} else if err := json.Unmarshal(data, &root); err != nil {
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: "TRAE mcp.json is not valid JSON; ask the organization technical administrator to repair it"}
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok && root["mcpServers"] != nil {
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: "TRAE mcpServers is not an object; its contract is not safe to modify"}
	}
	if servers == nil {
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	desired := map[string]any{"command": opt.binary, "args": []any{"--config", opt.serviceConfig}}
	if existing, exists := servers[instanceName]; exists {
		if equalServer(existing, desired) {
			return result{Client: clientName, Configured: true, ConfigPath: path, Result: "already_configured", NextAction: "restart TRAE if it is running"}
		}
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: "an existing TRAE server entry conflicts with requested paths; ask the organization technical administrator to inspect it"}
	}
	servers[instanceName] = desired
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	encoded = append(encoded, '\n')
	if newFile {
		if err := writeNew(path, encoded); err != nil {
			return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
		}
		return result{Client: clientName, Configured: true, ConfigPath: path, Result: "configured", NextAction: "restart TRAE, then run initialize, tools/list, and a read-only wecom_schema_status call"}
	}
	backup, err := backupAndReplace(path, encoded)
	if err != nil {
		return result{Client: clientName, ConfigPath: path, Result: "agent_blocked", NextAction: err.Error()}
	}
	return result{Client: clientName, Configured: true, ConfigPath: path, BackupPath: backup, Result: "configured", NextAction: "restart TRAE, then run initialize, tools/list, and a read-only wecom_schema_status call"}
}

func equalServer(raw any, desired map[string]any) bool {
	existing, ok := raw.(map[string]any)
	if !ok || existing["command"] != desired["command"] {
		return false
	}
	args, ok := existing["args"].([]any)
	return ok && len(args) == 2 && args[0] == "--config" && args[1] == desired["args"].([]any)[1]
}

func workBuddyBlocked(opt options) result {
	next := "WorkBuddy configuration contract/path is not established; inspect its MCP documentation and rerun with a supported adapter"
	if opt.workBuddyConfig != "" {
		next = "WorkBuddy adapter is intentionally unavailable because its configuration contract is unverified; do not edit the supplied path automatically"
	}
	return result{Client: "workbuddy", Result: "agent_blocked", NextAction: next}
}

func writeNew(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func backupAndReplace(path string, data []byte) (string, error) {
	if info, err := os.Lstat(path); err != nil {
		return "", err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("client configuration must be a regular file, not a symlink or special file")
	}
	backupBase := path + ".wecom-mcp-v2-" + time.Now().UTC().Format("20060102T150405Z")
	backup := backupBase + ".bak"
	for suffix := 1; ; suffix++ {
		if _, err := os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		backup = fmt.Sprintf("%s-%d.bak", backupBase, suffix)
	}
	if err := copyFile(path, backup); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".wecom-mcp-v2-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", err
	}
	return backup, nil
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func validateConfigTarget(path, label string, allowMissing bool) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path", label)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if !allowMissing {
			return fmt.Errorf("%s does not exist", label)
		}
		return validateConfigParent(filepath.Dir(path), label)
	}
	if err != nil {
		return fmt.Errorf("%s cannot be inspected: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symlink or special file", label)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("%s is group/other writable; tighten its permissions before registration", label)
	}
	return nil
}

func validateConfigParent(parent, label string) error {
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("parent directory for %s is not accessible: %w", label, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("parent directory for %s is not a directory", label)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("parent directory for %s is group/other writable; tighten its permissions before registration", label)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func printResults(results []result) {
	encoded, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(encoded))
}

func fatal(item result, code int) {
	printResults([]result{item})
	os.Exit(code)
}
