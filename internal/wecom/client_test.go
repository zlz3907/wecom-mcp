package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTextCell(t *testing.T) {
	if got := textCell([]any{map[string]any{"type": "text", "text": "ok"}}); got != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestSendAppMessageUsesManagedExecutor(t *testing.T) {
	var executorCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gnas/service/getJwtToken":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"token": "service-token", "expires_at": time.Now().Add(time.Hour).Unix()},
			})
		case "/gnas/service/wecomExecute":
			executorCalls++
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer service-token" || request.Header.Get("X-Auth-Type") != "service_jwt" || request.Header.Get("X-GNAS-Managed-Source") != "fixed-route" || request.Header.Get("X-GNAS-Upstream-Method") != http.MethodPost || request.Header.Get("X-GNAS-Upstream-Path") != "/cgi-bin/message/send" || !strings.HasPrefix(request.Header.Get("X-Request-ID"), "mcp-") {
				t.Errorf("invalid managed executor request: method=%s headers=%v", request.Method, request.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["touser"] != "recipient" || body["agentid"] != nil {
				t.Errorf("invalid managed executor body: %#v err=%v", body, err)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "msgid": "receipt-1"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, appID: "app", appSecret: "secret", route: "fixed-route", httpClient: server.Client()}
	response, err := client.Request(context.Background(), "send_app_message", map[string]any{"touser": "recipient", "msgtype": "text", "text": map[string]string{"content": "hello"}})
	if err != nil || executorCalls != 1 {
		t.Fatalf("managed executor request failed: calls=%d response=%#v err=%v", executorCalls, response, err)
	}
	result, _ := response["result"].(map[string]any)
	if result["msgid"] != "receipt-1" {
		t.Fatalf("unexpected managed executor response: %#v", response)
	}
}
