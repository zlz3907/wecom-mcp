package team

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

type requestIDKey struct{}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Auditor struct {
	logger *slog.Logger
	key    []byte
}

func NewAuditor(logger *slog.Logger, key []byte) *Auditor {
	return &Auditor{logger: logger, key: append([]byte(nil), key...)}
}

func (a *Auditor) ToolCall(ctx context.Context, tool string, role Role, started time.Time, err error) {
	if a == nil || a.logger == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "tool_error"
	}
	info := sdkauth.TokenInfoFromContext(ctx)
	a.logger.Info("team_mcp_tool_call",
		"request_id", requestID(ctx),
		"subject_hash", a.subjectHash(info),
		"role", string(role),
		"tool", tool,
		"outcome", outcome,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(value) {
			value = newRequestID()
		}
		w.Header().Set("X-Request-ID", value)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, value)))
	})
}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func newRequestID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(buffer)
}

func (a *Auditor) subjectHash(info *sdkauth.TokenInfo) string {
	if info == nil {
		return ""
	}
	issuer, _ := info.Extra["issuer"].(string)
	digest := hmac.New(sha256.New, a.key)
	_, _ = digest.Write([]byte(issuer + "\x00" + info.UserID))
	return hex.EncodeToString(digest.Sum(nil)[:16])
}
