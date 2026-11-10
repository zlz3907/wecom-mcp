package team

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The floor is authoritative only after the deployment controller installs
// the protected file and lock. Each authorization snapshots the generation;
// subsequent calls recheck it around fresh employee introspection.
const productionPolicyFloorPath = "/var/lib/wecom-mcp-policy-floor"
const maxPolicyFloorBytes = 1 << 20

type policyFloorDocument struct {
	SchemaVersion int    `json:"schema_version"`
	BindingKey    string `json:"binding_key"`
	Revision      string `json:"revision"`
	Generation    string `json:"generation"`
}

type policyFloorContextKey struct{}

type policyFloorDecision struct {
	Required   bool
	Revision   string
	Generation string
}

func policyFloorAppID(cfg Config) string {
	if !cfg.OAuth21ServiceJWT {
		return ""
	}
	return cfg.AuthorizationServiceAppID
}

func policyFloorBindingKey(app, tenant, resource, audience, issuer string) string {
	if app == "" {
		app = "~legacy"
	} else if app == "~legacy" {
		return ""
	}
	for _, value := range []string{app, tenant, resource, audience, issuer} {
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsRune(value, 0) {
			return ""
		}
	}
	canonical := func(value string) string {
		if validatePublicURL(value, "policy floor URL") != nil {
			return ""
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.RawPath != "" {
			return ""
		}
		parsed.Host = strings.ToLower(parsed.Host)
		return parsed.String()
	}
	audience, issuer = canonical(audience), canonical(issuer)
	if audience == "" || issuer == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(app + "\x00" + tenant + "\x00" + resource + "\x00" + audience + "\x00" + issuer))
	return hex.EncodeToString(sum[:])
}

func trustedFloorParents(path, root string, owner uint32) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	root = filepath.Clean(root)
	for dir := filepath.Dir(filepath.Clean(path)); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != owner {
			return false
		}
		if dir == root {
			return true
		}
		if dir == filepath.Dir(dir) {
			return false
		}
	}
}

func openTrustedFloorFile(path, root string, owner uint32) (*os.File, error) {
	if !trustedFloorParents(path, root, owner) {
		return nil, errors.New("untrusted policy floor parent")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		file.Close()
		return nil, errors.New("untrusted policy floor file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	current, currentErr := os.Lstat(path)
	if !ok || stat.Uid != owner || currentErr != nil || !os.SameFile(info, current) || !trustedFloorParents(path, root, owner) {
		file.Close()
		return nil, errors.New("changed policy floor file")
	}
	return file, nil
}

func acquirePolicyFloor(ctx context.Context, path, root string, owner uint32, key string) (policyFloorDecision, func(), error) {
	var zero policyFloorDecision
	if ctx == nil || !bindingDigestPattern.MatchString(key) {
		return zero, nil, errors.New("invalid policy floor request")
	}
	path = filepath.Join(path, key+".json")
	lock, err := openTrustedFloorFile(path+".lock", root, owner)
	if err != nil {
		return zero, nil, err
	}
	release := func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			lock.Close()
			return zero, nil, err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return zero, nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	info, err := lock.Stat()
	current, currentErr := os.Lstat(path + ".lock")
	if err != nil || currentErr != nil || !os.SameFile(info, current) || !trustedFloorParents(path, root, owner) {
		release()
		return zero, nil, errors.New("policy floor lock changed")
	}
	floor, err := openTrustedFloorFile(path, root, owner)
	if err != nil {
		release()
		return zero, nil, err
	}
	defer floor.Close()
	data, readErr := io.ReadAll(io.LimitReader(floor, maxPolicyFloorBytes+1))
	if readErr != nil || len(data) > maxPolicyFloorBytes {
		release()
		return zero, nil, errors.New("policy floor exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var doc policyFloorDocument
	begin, decodeErr := decoder.Token()
	if decodeErr != nil || begin != json.Delim('{') {
		release()
		return zero, nil, errors.New("invalid policy floor")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			release()
			return zero, nil, errors.New("duplicate or invalid policy floor field")
		}
		seen[name] = true
		switch name {
		case "schema_version":
			err = decoder.Decode(&doc.SchemaVersion)
		case "binding_key":
			err = decoder.Decode(&doc.BindingKey)
		case "revision":
			var revision *string
			err = decoder.Decode(&revision)
			if err == nil && revision == nil {
				err = errors.New("null floor revision")
			} else if err == nil {
				doc.Revision = *revision
			}
		case "generation":
			err = decoder.Decode(&doc.Generation)
		default:
			err = errors.New("unknown policy floor field")
		}
		if err != nil {
			release()
			return zero, nil, errors.New("invalid policy floor value")
		}
	}
	end, endErr := decoder.Token()
	if endErr != nil || end != json.Delim('}') || decoder.Decode(&struct{}{}) != io.EOF || len(seen) != 4 || doc.SchemaVersion != 1 || doc.BindingKey != key || !managedPolicyRevisionPattern.MatchString(doc.Generation) || (doc.Revision != "" && !managedPolicyRevisionPattern.MatchString(doc.Revision)) {
		release()
		return zero, nil, errors.New("invalid policy floor")
	}
	return policyFloorDecision{Required: doc.Revision != "", Revision: doc.Revision, Generation: doc.Generation}, release, nil
}

func policyFloorMiddlewareAt(next http.Handler, path, root string, owner uint32, key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decision, release, err := acquirePolicyFloor(r.Context(), path, root, owner, key)
		if err != nil {
			http.Error(w, "policy authorization unavailable", http.StatusServiceUnavailable)
			return
		}
		release()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), policyFloorContextKey{}, decision)))
	})
}
