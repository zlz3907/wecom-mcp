package team

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

type policyToolRevalidator func(context.Context, string, requestAuthorization) (requestAuthorization, error)
type policyToolRevalidatorKey struct{}

// The token is kept in the request context only and is never written to logs.
// Each tool call obtains fresh WeCom membership and policy decisions, including
// calls within a batch or a long-lived client connection.
func (s *Service) withPolicyToolRevalidation(next http.Handler, verifier sdkauth.TokenVerifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			http.Error(w, "authorization unavailable", http.StatusUnauthorized)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			http.Error(w, "authorization unavailable", http.StatusUnauthorized)
			return
		}
		token := parts[1]
		check := policyToolRevalidator(func(ctx context.Context, tool string, prior requestAuthorization) (requestAuthorization, error) {
			return s.revalidatePolicyTool(ctx, verifier, token, r, tool, prior, s.config.PolicyFloorPath, s.policyFloorRoot, s.policyFloorOwner)
		})
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), policyToolRevalidatorKey{}, check)))
	})
}

func (s *Service) revalidatePolicyTool(ctx context.Context, verifier sdkauth.TokenVerifier, token string, request *http.Request, tool string, prior requestAuthorization, path, root string, owner uint32) (requestAuthorization, error) {
	var zero requestAuthorization
	var key string
	var before policyFloorDecision
	if path != "" {
		key = policyFloorBindingKey(policyFloorAppID(s.config), s.config.AuthorizationTenant, s.config.AuthorizationResource, s.config.MCPURL, s.config.OIDCIssuer)
		var release func()
		var err error
		before, release, err = acquirePolicyFloor(ctx, path, root, owner, key)
		if err != nil {
			return zero, err
		}
		release()
		ctx = context.WithValue(ctx, policyFloorContextKey{}, before)
	}
	info, err := verifier(ctx, token, request)
	if err != nil {
		return zero, err
	}
	decision, err := authorizationFromOAuth21TokenInfo(info, s.config, time.Now().UTC())
	if err != nil || decision.UserID != prior.decision.UserID || decision.Tenant != prior.decision.Tenant || decision.Resource != prior.decision.Resource || (path != "" && (before.Required != decision.ManagedToolPolicy || (before.Required && (!decision.PolicyContractV1 || before.Revision != decision.PolicyVersion)))) {
		return zero, errors.New("current employee policy does not match")
	}
	tools, err := authorizedToolSet(decision, s.definitions, oauth21NonZoopScopes(s.config.RequiredScopes))
	if err != nil || !tools[tool] {
		return zero, errors.New("tool is not authorized for current employee policy")
	}
	if path != "" {
		after, release, err := acquirePolicyFloor(ctx, path, root, owner, key)
		if err != nil {
			return zero, err
		}
		release()
		if before != after {
			return zero, errors.New("employee policy changed during authorization")
		}
	}
	return requestAuthorization{decision: decision, tools: tools}, nil
}
