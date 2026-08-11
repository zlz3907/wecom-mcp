package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Field struct {
	Title                  string            `json:"field_title"`
	ID                     string            `json:"field_id"`
	Type                   string            `json:"field_type"`
	Options                map[string]string `json:"select_options,omitempty"`
	ReferenceTargetSheetID string            `json:"reference_target_sheet_id,omitempty"`
	ReferenceTargetFieldID string            `json:"reference_target_field_id,omitempty"`
	ReferenceIsMultiple    *bool             `json:"reference_is_multiple,omitempty"`
}
type Schema struct {
	Roles  map[string]map[string]Field
	Digest string
}

var roleHeading = regexp.MustCompile(`^## (Z-S0[1-9])｜`)
var roleCode = regexp.MustCompile(`^Z-S0[1-9]$`)

// LoadSchema reads a local Markdown or generated JSON mirror. It never
// contacts WeCom or modifies the mirror; Owner-authorized sync is external.
func LoadSchema(path string) (Schema, error) {
	if filepath.Ext(path) == ".json" {
		return loadJSONSchema(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return Schema{}, fmt.Errorf("读取 Schema 镜像失败: %w", err)
	}
	defer file.Close()
	result := Schema{Roles: map[string]map[string]Field{}}
	currentRole := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if match := roleHeading.FindStringSubmatch(line); len(match) == 2 {
			currentRole = match[1]
			result.Roles[currentRole] = map[string]Field{}
			continue
		}
		if currentRole == "" || !strings.HasPrefix(line, "|") || strings.Contains(line, "Field ID") || strings.HasPrefix(line, "| ---") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		title, id, typ := strings.TrimSpace(parts[1]), strings.Trim(strings.TrimSpace(parts[2]), "`"), strings.Trim(strings.TrimSpace(parts[3]), "`")
		if title != "" && identifier.MatchString(id) && strings.HasPrefix(typ, "FIELD_TYPE_") {
			if _, exists := result.Roles[currentRole][title]; exists {
				return Schema{}, fmt.Errorf("Schema 镜像中 %s 字段 %s 重复", currentRole, title)
			}
			result.Roles[currentRole][title] = Field{Title: title, ID: id, Type: typ}
		}
	}
	if err := scanner.Err(); err != nil {
		return Schema{}, fmt.Errorf("读取 Schema 镜像失败: %w", err)
	}
	for index := 1; index <= 8; index++ {
		role := fmt.Sprintf("Z-S0%d", index)
		if len(result.Roles[role]) == 0 {
			return Schema{}, fmt.Errorf("Schema 镜像缺少 %s", role)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Schema{}, err
	}
	sum := sha256.Sum256(data)
	result.Digest = hex.EncodeToString(sum[:])
	return result, nil
}

type onlineMirror struct {
	Version    int                   `json:"version"`
	CapturedAt string                `json:"captured_at"`
	Roles      map[string]mirrorRole `json:"roles"`
}
type mirrorRole struct {
	Fields []Field `json:"fields"`
}

func loadJSONSchema(path string) (Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Schema{}, fmt.Errorf("读取 Schema 镜像失败: %w", err)
	}
	var mirror onlineMirror
	if err := json.Unmarshal(data, &mirror); err != nil || mirror.Version != 1 {
		return Schema{}, fmt.Errorf("Schema JSON 镜像无效")
	}
	result := Schema{Roles: map[string]map[string]Field{}}
	for index := 1; index <= 8; index++ {
		role := fmt.Sprintf("Z-S0%d", index)
		if source, found := mirror.Roles[role]; !found || len(source.Fields) == 0 {
			return Schema{}, fmt.Errorf("Schema JSON 镜像缺少 %s", role)
		}
	}
	roles := make([]string, 0, len(mirror.Roles))
	for role := range mirror.Roles {
		if roleCode.MatchString(role) {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	for _, role := range roles {
		source := mirror.Roles[role]
		if len(source.Fields) == 0 {
			return Schema{}, fmt.Errorf("Schema JSON 镜像缺少 %s", role)
		}
		result.Roles[role] = map[string]Field{}
		for _, field := range source.Fields {
			if field.Title == "" || !identifier.MatchString(field.ID) || !strings.HasPrefix(field.Type, "FIELD_TYPE_") {
				return Schema{}, fmt.Errorf("Schema JSON 镜像字段无效")
			}
			if _, duplicate := result.Roles[role][field.Title]; duplicate {
				return Schema{}, fmt.Errorf("Schema JSON 镜像字段重复")
			}
			result.Roles[role][field.Title] = field
		}
	}
	sum := sha256.Sum256(data)
	result.Digest = hex.EncodeToString(sum[:])
	return result, nil
}

// WriteOnlineMirror atomically persists only the field contract needed by the
// MCP. It deliberately excludes document and sheet identifiers.
func WriteOnlineMirror(path string, fields map[string][]Field, capturedAt string) error {
	roles := make(map[string]mirrorRole, len(fields))
	for role, items := range fields {
		copyItems := append([]Field(nil), items...)
		sort.Slice(copyItems, func(i, j int) bool { return copyItems[i].ID < copyItems[j].ID })
		roles[role] = mirrorRole{Fields: copyItems}
	}
	data, err := json.MarshalIndent(onlineMirror{Version: 1, CapturedAt: capturedAt, Roles: roles}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// WriteReadableOnlineMirror writes a human-readable sibling of the generated
// JSON mirror from the exact same online field snapshot. The JSON mirror
// remains the only runtime input; this file is an inspection aid and must not
// be edited or used to infer unsupported write codecs.
func WriteReadableOnlineMirror(path string, fields map[string][]Field, capturedAt string) error {
	if filepath.Ext(path) != ".md" {
		return fmt.Errorf("可读 Schema 镜像必须是 Markdown 文件")
	}
	roles := make([]string, 0, len(fields))
	for role := range fields {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	var text strings.Builder
	text.WriteString("# Zoop：线上 Schema 可读镜像\n\n")
	text.WriteString("**生成时间**：")
	text.WriteString(capturedAt)
	text.WriteString("  \n**状态**：由 Owner 授权的线上只读同步生成  \n\n")
	text.WriteString("> 本文件与同名 JSON 使用同一线上字段快照生成。JSON 是 MCP 唯一执行镜像；本文件只供人工检查，不得手工修改，也不得用于推测未验证字段的写入编码。\n\n")
	text.WriteString("## 表级摘要\n\n| 表代码 | 字段数 |\n|---|---:|\n")
	for _, role := range roles {
		text.WriteString(fmt.Sprintf("| %s | %d |\n", role, len(fields[role])))
	}
	for _, role := range roles {
		items := append([]Field(nil), fields[role]...)
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		text.WriteString("\n## ")
		text.WriteString(role)
		text.WriteString("｜线上字段\n\n| 字段 | Field ID | 类型 | 线上属性 |\n|---|---|---|---|\n")
		for _, field := range items {
			text.WriteString("| ")
			text.WriteString(escapeMarkdownCell(field.Title))
			text.WriteString(" | `")
			text.WriteString(field.ID)
			text.WriteString("` | `")
			text.WriteString(field.Type)
			text.WriteString("` | ")
			text.WriteString(escapeMarkdownCell(readableFieldProperty(field)))
			text.WriteString(" |\n")
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(text.String()), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func readableFieldProperty(field Field) string {
	parts := []string{}
	if len(field.Options) > 0 {
		labels := make([]string, 0, len(field.Options))
		for label := range field.Options {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		parts = append(parts, "选项："+strings.Join(labels, "、"))
	}
	if field.ReferenceTargetSheetID != "" {
		parts = append(parts, "关联目标子表：`"+field.ReferenceTargetSheetID+"`")
	}
	if field.ReferenceTargetFieldID != "" {
		parts = append(parts, "目标字段：`"+field.ReferenceTargetFieldID+"`")
	}
	if field.ReferenceIsMultiple != nil {
		parts = append(parts, fmt.Sprintf("多选：%t", *field.ReferenceIsMultiple))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, "；")
}

func escapeMarkdownCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}
