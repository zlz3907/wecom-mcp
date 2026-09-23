package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTextCell(t *testing.T) {
	if got := textCell([]any{map[string]any{"type": "text", "text": "ok"}}); got != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestUploadAppMediaUsesManagedMultipartExecutor(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 64)...)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gnas/service/getJwtToken":
			_ = json.NewEncoder(writer).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "service-token", "expires_at": time.Now().Add(time.Hour).Unix()}})
		case "/gnas/service/wecomExecute":
			if request.Header.Get("X-GNAS-Upstream-Path") != "/cgi-bin/media/upload?type=image" || request.Header.Get("X-GNAS-Managed-Source") != "fixed-route" {
				t.Errorf("unexpected managed upload headers: %v", request.Header)
			}
			mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/form-data" {
				t.Fatalf("invalid upload content type: %q err=%v", mediaType, err)
			}
			part, err := multipart.NewReader(request.Body, parameters["boundary"]).NextPart()
			if err != nil || part.FormName() != "media" || part.FileName() != "probe.png" {
				t.Fatalf("invalid upload part: part=%#v err=%v", part, err)
			}
			rawDisposition := part.Header.Get("Content-Disposition")
			if !strings.HasPrefix(rawDisposition, `form-data; name="media"; filename="probe.png"; filelength=`) {
				t.Fatalf("upload metadata order does not match the Enterprise WeCom media contract: %q", rawDisposition)
			}
			disposition, dispositionParameters, err := mime.ParseMediaType(rawDisposition)
			if err != nil || disposition != "form-data" || dispositionParameters["filelength"] != strconv.Itoa(len(png)) {
				t.Fatalf("invalid upload part metadata: disposition=%q parameters=%v err=%v", disposition, dispositionParameters, err)
			}
			got, _ := io.ReadAll(part)
			if !bytes.Equal(got, png) {
				t.Fatal("upload content changed")
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "type": "image", "media_id": "media-1", "created_at": 1})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, appID: "app", appSecret: "secret", route: "fixed-route", httpClient: server.Client()}
	response, err := client.UploadAppMedia(context.Background(), "image", "probe.png", png)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	if result["media_id"] != "media-1" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestUploadAppMediaRejectsUnsafeOrOversizedInput(t *testing.T) {
	client := &Client{}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 64)...)
	for _, test := range []struct {
		mediaType string
		filename  string
		content   []byte
	}{
		{"video", "probe.mp4", []byte("content")},
		{"image", "../probe.png", png},
		{"image", "probe.jpg", png},
		{"file", "empty.bin", []byte("12345")},
		{"image", "large.png", make([]byte, MaxAppImageBytes+1)},
		{"file", "large.bin", make([]byte, MaxAppFileBytes+1)},
	} {
		if _, err := client.UploadAppMedia(context.Background(), test.mediaType, test.filename, test.content); err == nil {
			t.Fatalf("unsafe media accepted: type=%q filename=%q size=%d", test.mediaType, test.filename, len(test.content))
		}
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

func TestGetEmployeeUsesFixedRouteAndEncodedUserID(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(strconv.FormatBool(managed), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/gnas/service/getJwtToken" {
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "fixture-token", "expires_at": time.Now().Add(time.Hour).Unix()}})
					return
				}
				calls++
				if managed {
					if r.URL.Path != "/gnas/service/wecomExecute" || r.Method != "POST" || r.Header.Get("X-GNAS-Managed-Source") != "fixed-route" || r.Header.Get("X-GNAS-Upstream-Method") != "GET" || r.Header.Get("X-GNAS-Upstream-Path") != "/cgi-bin/user/get?userid=operator%40example.invalid" {
						t.Errorf("unexpected managed route")
					}
				} else if r.Method != "GET" || r.URL.RequestURI() != "/api/fixed-route/cgi-bin/user/get?userid=operator%40example.invalid" {
					t.Errorf("unexpected legacy route: %s", r.URL.RequestURI())
				}
				body, _ := io.ReadAll(r.Body)
				if len(body) != 0 {
					t.Error("GET lookup must have no body")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "userid": "operator@example.invalid", "status": 1})
			}))
			defer server.Close()
			client := &Client{baseURL: server.URL, appID: "app", appSecret: "fixture", route: "fixed-route", httpClient: server.Client(), managedExecutor: managed}
			_, err := client.Request(context.Background(), "get_employee", map[string]any{"userid": "operator@example.invalid"})
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestGetEmployeeRejectsInvalidPayloadBeforeTransport(t *testing.T) {
	client := &Client{}
	for _, payload := range []any{nil, map[string]any{}, map[string]any{"userid": ""}, map[string]any{"userid": 1}, map[string]any{"userid": "x&access_token=y"}, map[string]any{"userid": "x\r\ny"}, map[string]any{"userid": strings.Repeat("x", 65)}, map[string]any{"userid": "valid", "source": "other"}} {
		if _, err := client.Request(context.Background(), "get_employee", payload); err == nil {
			t.Fatalf("accepted invalid payload: %#v", payload)
		}
	}
}
