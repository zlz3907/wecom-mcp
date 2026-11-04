package team

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// BindingWorkerProtocolVersion is deliberately independent from the external
// fleet contract.  The protocol is used only over the parent/child private
// pipe; it must never be exposed through an MCP endpoint or operator UI.
const BindingWorkerProtocolVersion = 1

// BindingWorkerSpec is the non-secret identity portion of a child start
// request.  The parent passes runtime configuration through its protected
// process boundary in a later step; this structure is intentionally limited
// to the values that must be authenticated before a reverse proxy is promoted.
type BindingWorkerSpec struct {
	Version       int    `json:"version"`
	BindingID     string `json:"binding_id"`
	BindingDigest string `json:"binding_digest"`
	Tenant        string `json:"tenant"`
	Resource      string `json:"resource"`
	PublicURL     string `json:"public_url"`
}

// BindingWorkerReady is the only stdout message accepted from a child.  MAC
// authenticates the exact identity and loopback address; it is computed with
// a fresh, per-child random secret that is never logged or put in argv.
type BindingWorkerReady struct {
	Version       int    `json:"version"`
	BindingDigest string `json:"binding_digest"`
	Tenant        string `json:"tenant"`
	Resource      string `json:"resource"`
	Address       string `json:"address"`
	MAC           string `json:"mac"`
}

// NewBindingWorkerSpec rejects drift between the authoritative route and the
// Config that would be used to build the child.  It carries no tokens, keys,
// registry coordinates, or Source references.
func NewBindingWorkerSpec(binding LoadedFleetBinding) (BindingWorkerSpec, error) {
	spec := BindingWorkerSpec{
		Version:       BindingWorkerProtocolVersion,
		BindingID:     binding.Binding.BindingID,
		BindingDigest: binding.Config.GNASBindingDigest,
		Tenant:        binding.Config.AuthorizationTenant,
		Resource:      binding.Config.AuthorizationResource,
		PublicURL:     binding.Binding.PublicURL,
	}
	if spec.Tenant != binding.Binding.AuthorizationTenant || spec.Resource != binding.Binding.AuthorizationResource || binding.Config.PublicURL != binding.Binding.PublicURL {
		return BindingWorkerSpec{}, fmt.Errorf("binding worker identity does not match authoritative route")
	}
	if err := spec.Validate(); err != nil {
		return BindingWorkerSpec{}, err
	}
	return spec, nil
}

func (s BindingWorkerSpec) Validate() error {
	if s.Version != BindingWorkerProtocolVersion || !fleetIdentifier.MatchString(s.BindingID) || !bindingDigestPattern.MatchString(s.BindingDigest) || !fleetIdentifier.MatchString(s.Tenant) || !fleetIdentifier.MatchString(s.Resource) || s.BindingID != s.Tenant {
		return fmt.Errorf("binding worker identity is invalid")
	}
	if hasWorkerControl(s.BindingID) || hasWorkerControl(s.BindingDigest) || hasWorkerControl(s.Tenant) || hasWorkerControl(s.Resource) || hasWorkerControl(s.PublicURL) {
		return fmt.Errorf("binding worker identity contains control characters")
	}
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !fleetHost.MatchString(u.Hostname()) {
		return fmt.Errorf("binding worker public URL is invalid")
	}
	if normalized, err := normalizeTeamPublicURL(s.PublicURL); err != nil || normalized != s.PublicURL {
		return fmt.Errorf("binding worker public URL is not canonical")
	}
	return nil
}

// SignBindingWorkerReady returns a fixed-size HMAC for the canonical ready
// tuple.  The MAC field itself is deliberately excluded from the input.
func SignBindingWorkerReady(secret []byte, ready BindingWorkerReady) (string, error) {
	if len(secret) < 32 {
		return "", fmt.Errorf("binding worker handshake secret is too short")
	}
	if err := ready.validateUnsigned(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Version       int    `json:"version"`
		BindingDigest string `json:"binding_digest"`
		Tenant        string `json:"tenant"`
		Resource      string `json:"resource"`
		Address       string `json:"address"`
	}{ready.Version, ready.BindingDigest, ready.Tenant, ready.Resource, ready.Address})
	if err != nil {
		return "", fmt.Errorf("marshal binding worker handshake: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyBindingWorkerReady verifies the child response and exact parent
// expectation before a proxy can receive any traffic.
func VerifyBindingWorkerReady(secret []byte, expected BindingWorkerSpec, ready BindingWorkerReady) error {
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("invalid expected binding worker identity: %w", err)
	}
	if err := ready.validateUnsigned(); err != nil {
		return err
	}
	if ready.Version != expected.Version || ready.BindingDigest != expected.BindingDigest || ready.Tenant != expected.Tenant || ready.Resource != expected.Resource {
		return fmt.Errorf("binding worker ready identity mismatch")
	}
	provided, err := hex.DecodeString(ready.MAC)
	if err != nil || len(provided) != sha256.Size {
		return fmt.Errorf("binding worker ready MAC is invalid")
	}
	want, err := SignBindingWorkerReady(secret, BindingWorkerReady{
		Version: ready.Version, BindingDigest: ready.BindingDigest, Tenant: ready.Tenant, Resource: ready.Resource, Address: ready.Address,
	})
	if err != nil {
		return err
	}
	expectedMAC, _ := hex.DecodeString(want)
	if !hmac.Equal(provided, expectedMAC) {
		return fmt.Errorf("binding worker ready MAC mismatch")
	}
	return nil
}

func (r BindingWorkerReady) validateUnsigned() error {
	if r.Version != BindingWorkerProtocolVersion || !bindingDigestPattern.MatchString(r.BindingDigest) || !fleetIdentifier.MatchString(r.Tenant) || !fleetIdentifier.MatchString(r.Resource) || hasWorkerControl(r.BindingDigest) || hasWorkerControl(r.Tenant) || hasWorkerControl(r.Resource) || hasWorkerControl(r.Address) {
		return fmt.Errorf("binding worker ready identity is invalid")
	}
	host, port, err := net.SplitHostPort(r.Address)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("binding worker ready address is invalid")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("binding worker ready address must be loopback")
	}
	return nil
}

func hasWorkerControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r <= 0x1f || r == 0x7f }) >= 0
}
