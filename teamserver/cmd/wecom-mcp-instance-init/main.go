// wecom-mcp-instance-init performs the one-time, local-only initialization
// of a fixed Enterprise WeCom instance. It deliberately does not open an HTTP
// listener or accept credentials: systemd/administrator supplies the existing
// environment, and the configured schema_admin_user remains the identity gate.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

type toolResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
	} `json:"result"`
}

func main() {
	configPath := flag.String("config", "", "absolute fixed-tenant instance configuration path")
	apply := flag.Bool("apply", false, "apply the current initialization preview after a fresh dry-run")
	flag.Parse()
	if *configPath == "" || (*configPath)[0] != '/' {
		fmt.Fprintln(os.Stderr, "error: --config must be an absolute path")
		os.Exit(2)
	}

	server := legacymcp.New(*configPath)
	status, err := call(context.Background(), server, "wecom_instance_initialize_status", map[string]string{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialization status failed:", err)
		os.Exit(1)
	}
	if !*apply {
		write(status)
		return
	}
	previewID, _ := status["preview_id"].(string)
	expiresAt, _ := status["expires_at"].(string)
	if previewID == "" || expiresAt == "" {
		fmt.Fprintln(os.Stderr, "error: current state has no executable initialization preview")
		write(status)
		os.Exit(1)
	}
	applied, err := call(context.Background(), server, "wecom_instance_initialize_apply", map[string]string{
		"preview_id":          previewID,
		"preview_expires_at":  expiresAt,
		"owner_authorization": "initialize_or_reconcile_default_zoop_instance",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialization apply failed:", err)
		os.Exit(1)
	}
	write(applied)
}

func call(ctx context.Context, server *legacymcp.Server, name string, arguments any) (map[string]any, error) {
	rawArguments, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": json.RawMessage(rawArguments)},
	})
	if err != nil {
		return nil, err
	}
	response := server.Handle(ctx, request)
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	var decoded toolResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("MCP protocol error: %s", decoded.Error.Message)
	}
	if len(decoded.Result.StructuredContent) == 0 {
		return nil, fmt.Errorf("MCP tool returned no structured result")
	}
	var result map[string]any
	if err := json.Unmarshal(decoded.Result.StructuredContent, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func write(value map[string]any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot encode result")
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}
