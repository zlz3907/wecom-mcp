// Package wecom provides the fixed-route GNAS-to-WeCom transport.
package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const requestTimeout = 10 * time.Second
const maxResponseBytes = 1024 * 1024

type Operation struct {
	Method string
	Path   string
	Kind   string
}

// Operations is the complete Enterprise WeCom surface implemented by the
// legacy MCP. get_sheet is retained as a compatibility alias for its legacy
// public name get_sheets; both call the same upstream endpoint.
var Operations = map[string]Operation{
	"list_employees":       {"GET", "/cgi-bin/user/list?department_id=1&fetch_child=1", "read"},
	"get_doc_base_info":    {"POST", "/cgi-bin/wedoc/get_doc_base_info", "read"},
	"get_doc_share_url":    {"POST", "/cgi-bin/wedoc/doc_share", "read"},
	"get_doc_auth":         {"POST", "/cgi-bin/wedoc/doc_get_auth", "read"},
	"get_sheets":           {"POST", "/cgi-bin/wedoc/smartsheet/get_sheet", "read"},
	"get_sheet":            {"POST", "/cgi-bin/wedoc/smartsheet/get_sheet", "read"},
	"get_views":            {"POST", "/cgi-bin/wedoc/smartsheet/get_views", "read"},
	"get_fields":           {"POST", "/cgi-bin/wedoc/smartsheet/get_fields", "read"},
	"get_records":          {"POST", "/cgi-bin/wedoc/smartsheet/get_records", "read"},
	"create_smartsheet":    {"POST", "/cgi-bin/wedoc/create_doc", "write"},
	"rename_document":      {"POST", "/cgi-bin/wedoc/rename_doc", "write"},
	"lock_down_doc_access": {"POST", "/cgi-bin/wedoc/mod_doc_join_rule", "write"},
	"grant_doc_readers":    {"POST", "/cgi-bin/wedoc/mod_doc_member", "write"},
	"harden_doc_security":  {"POST", "/cgi-bin/wedoc/mod_doc_safty_setting", "write"},
	"add_sheet":            {"POST", "/cgi-bin/wedoc/smartsheet/add_sheet", "write"},
	"update_sheet":         {"POST", "/cgi-bin/wedoc/smartsheet/update_sheet", "write"},
	"delete_sheet":         {"POST", "/cgi-bin/wedoc/smartsheet/delete_sheet", "write"},
	"add_view":             {"POST", "/cgi-bin/wedoc/smartsheet/add_view", "write"},
	"update_view":          {"POST", "/cgi-bin/wedoc/smartsheet/update_view", "write"},
	"delete_views":         {"POST", "/cgi-bin/wedoc/smartsheet/delete_views", "write"},
	"add_fields":           {"POST", "/cgi-bin/wedoc/smartsheet/add_fields", "write"},
	"update_fields":        {"POST", "/cgi-bin/wedoc/smartsheet/update_fields", "write"},
	"delete_fields":        {"POST", "/cgi-bin/wedoc/smartsheet/delete_fields", "write"},
	"add_records":          {"POST", "/cgi-bin/wedoc/smartsheet/add_records", "write"},
	"update_records":       {"POST", "/cgi-bin/wedoc/smartsheet/update_records", "write"},
	"delete_records":       {"POST", "/cgi-bin/wedoc/smartsheet/delete_records", "write"},
}

type Client struct {
	baseURL, appID, appSecret, route string
	httpClient                       *http.Client
	mu                               sync.Mutex
	token                            string
	expiresAt                        int64
}

func NewFromEnvironment(route string) (*Client, error) {
	baseURL, appID, appSecret := strings.TrimSpace(os.Getenv("GNAS_BASE_URL")), strings.TrimSpace(os.Getenv("GNAS_APP_ID")), os.Getenv("GNAS_APP_SECRET")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("GNAS_BASE_URL 必须是 HTTPS 根地址")
	}
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("GNAS 服务凭据未配置")
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), appID: appID, appSecret: appSecret, route: route, httpClient: &http.Client{Timeout: requestTimeout}}, nil
}

func (c *Client) jwt(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && c.expiresAt > time.Now().Unix()+60 {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/gnas/service/getJwtToken", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构造 GNAS JWT 请求失败")
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GNAS JWT 请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GNAS JWT 请求失败: HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Code int `json:"code"`
		Data struct {
			Token     string `json:"token"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"data"`
	}
	if err := decodeBounded(resp.Body, &parsed); err != nil {
		return "", fmt.Errorf("GNAS JWT 响应无效")
	}
	if parsed.Code != 200 || parsed.Data.Token == "" || parsed.Data.ExpiresAt <= time.Now().Unix()+60 {
		return "", fmt.Errorf("GNAS JWT 响应不可用")
	}
	c.token, c.expiresAt = parsed.Data.Token, parsed.Data.ExpiresAt
	return c.token, nil
}

// Request accepts only names in the compiled allowlist. The instance route is
// fixed at construction and cannot be supplied by an MCP caller.
func (c *Client) Request(ctx context.Context, operation string, payload any) (map[string]any, error) {
	definition, ok := Operations[operation]
	if !ok {
		return nil, fmt.Errorf("不支持的企业微信 API: %s", operation)
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.jwt(ctx, attempt == 1)
		if err != nil {
			return nil, err
		}
		var body io.Reader
		if definition.Method != http.MethodGet {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, fmt.Errorf("企业微信请求编码失败")
			}
			body = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, definition.Method, c.baseURL+"/api/"+c.route+definition.Path, body)
		if err != nil {
			return nil, fmt.Errorf("构造企业微信请求失败")
		}
		req.Header.Set("authorization", "Bearer "+token)
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("企业微信受管请求失败")
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		defer resp.Body.Close()
		var upstream map[string]any
		if err := decodeBounded(resp.Body, &upstream); err != nil {
			return nil, fmt.Errorf("企业微信响应无效")
		}
		return map[string]any{"result": upstream, "_http_status": resp.StatusCode}, nil
	}
	return nil, fmt.Errorf("企业微信认证重试失败")
}

func decodeBounded(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("响应超过安全大小")
	}
	return json.Unmarshal(data, target)
}
