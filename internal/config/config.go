// Package config loads one fixed-tenant MCP instance configuration.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

var supportedOperations = func() map[string]struct{} {
	operations := map[string]struct{}{}
	for name := range wecom.Operations {
		operations[name] = struct{}{}
	}
	return operations
}()

type Config struct {
	Version            int                 `json:"version"`
	InstanceName       string              `json:"instance_name"`
	TenantRoute        string              `json:"tenant_route"`
	SchemaAdminUser    string              `json:"schema_admin_user,omitempty"`
	RegistryDocumentID string              `json:"registry_document_id"`
	RegistryKey        string              `json:"registry_key"`
	SchemaMirrorPath   string              `json:"schema_mirror_path"`
	StatePath          string              `json:"state_path"`
	APIWhitelist       map[string][]string `json:"api_whitelist"`
}

func (c Config) Validate() error {
	return c.validate(true)
}

// ValidateBootstrapCandidate validates every fixed-tenant boundary while
// allowing registry_document_id to be empty for the explicit bootstrap tool.
// Normal runtime calls must continue to use Validate and fail closed.
func (c Config) ValidateBootstrapCandidate() error {
	return c.validate(false)
}

func (c Config) validate(requireRegistry bool) error {
	if c.Version != 1 {
		return fmt.Errorf("配置 version 必须为 1")
	}
	for name, value := range map[string]string{
		"instance_name": c.InstanceName, "tenant_route": c.TenantRoute, "registry_key": c.RegistryKey,
	} {
		if !identifier.MatchString(value) {
			return fmt.Errorf("配置 %s 非法", name)
		}
	}
	if c.SchemaAdminUser != "" && !identifier.MatchString(c.SchemaAdminUser) {
		return fmt.Errorf("配置 schema_admin_user 非法")
	}
	if requireRegistry || c.RegistryDocumentID != "" {
		if !identifier.MatchString(c.RegistryDocumentID) {
			return fmt.Errorf("配置 registry_document_id 非法")
		}
	}
	if (!strings.HasSuffix(c.SchemaMirrorPath, ".md") && !strings.HasSuffix(c.SchemaMirrorPath, ".json")) || !filepath.IsAbs(c.SchemaMirrorPath) {
		return fmt.Errorf("schema_mirror_path 必须是绝对 Markdown 或 JSON 文件路径")
	}
	if !filepath.IsAbs(c.StatePath) {
		return fmt.Errorf("state_path 必须是绝对路径")
	}
	if len(c.APIWhitelist) == 0 {
		return fmt.Errorf("api_whitelist 不能为空")
	}
	for group, operations := range c.APIWhitelist {
		if group == "" || len(operations) == 0 {
			return fmt.Errorf("白名单组 %q 不能为空", group)
		}
		for _, operation := range operations {
			if _, ok := supportedOperations[operation]; !ok {
				return fmt.Errorf("白名单包含不支持的 API: %s", operation)
			}
		}
	}
	return nil
}

func Load(path string) (Config, error) {
	return load(path, true)
}

func LoadBootstrapCandidate(path string) (Config, error) {
	return load(path, false)
}

func load(path string, requireRegistry bool) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取实例配置失败: %w", err)
	}
	var result Config
	if err := json.Unmarshal(data, &result); err != nil {
		return Config{}, fmt.Errorf("实例配置不是合法 JSON: %w", err)
	}
	var validationError error
	if requireRegistry {
		validationError = result.Validate()
	} else {
		validationError = result.ValidateBootstrapCandidate()
	}
	if validationError != nil {
		return Config{}, validationError
	}
	return result, nil
}

func (c Config) Allows(operation string) bool {
	for _, group := range c.APIWhitelist {
		for _, allowed := range group {
			if allowed == operation {
				return true
			}
		}
	}
	return false
}

// AllowsInGroup keeps privileged operations scoped to their named capability.
// A schema migration must not become callable through a generic API merely
// because another allowlist group contains the same upstream operation.
func (c Config) AllowsInGroup(group, operation string) bool {
	for _, allowed := range c.APIWhitelist[group] {
		if allowed == operation {
			return true
		}
	}
	return false
}

func (c Config) Digest() string {
	groups := make([]string, 0, len(c.APIWhitelist))
	for name, operations := range c.APIWhitelist {
		copyOperations := append([]string(nil), operations...)
		sort.Strings(copyOperations)
		groups = append(groups, name+":"+strings.Join(copyOperations, ","))
	}
	sort.Strings(groups)
	sum := sha256.Sum256([]byte(strings.Join([]string{c.InstanceName, c.TenantRoute, c.SchemaAdminUser, c.RegistryDocumentID, c.RegistryKey, c.SchemaMirrorPath, strings.Join(groups, ";")}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Store re-reads configuration whenever its modification time changes. A bad
// replacement fails closed rather than silently using an earlier allowlist.
type Store struct {
	path    string
	mu      sync.Mutex
	lastMod int64
	cached  Config
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) BootstrapCandidate() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return LoadBootstrapCandidate(s.path)
}

// PersistRegistryDocumentID atomically fills an empty registry_document_id.
// It never overwrites an existing different target and preserves the config
// file's permissions.
func (s *Store) PersistRegistryDocumentID(documentID string) error {
	if !identifier.MatchString(documentID) {
		return fmt.Errorf("待写回的 registry_document_id 非法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := LoadBootstrapCandidate(s.path)
	if err != nil {
		return err
	}
	if current.RegistryDocumentID != "" {
		if current.RegistryDocumentID == documentID {
			return nil
		}
		return fmt.Errorf("registry_document_id 已指向其他文档，拒绝覆盖")
	}
	current.RegistryDocumentID = documentID
	if err := current.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".registry-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return err
	}
	s.cached = Config{}
	s.lastMod = 0
	return nil
}

func (s *Store) Current() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Stat(s.path)
	if err != nil {
		return Config{}, fmt.Errorf("读取实例配置状态失败: %w", err)
	}
	mod := info.ModTime().UnixNano()
	if mod != s.lastMod || s.cached.InstanceName == "" {
		loaded, err := Load(s.path)
		if err != nil {
			return Config{}, err
		}
		s.cached, s.lastMod = loaded, mod
	}
	return s.cached, nil
}
