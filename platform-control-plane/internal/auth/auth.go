package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
)

const HeaderApprovalSignature = "X-Platform-Approval-Signature"

type Role string

const (
	RoleViewer    Role = "viewer"
	RoleRequester Role = "requester"
	RoleApprover  Role = "approver"
	RoleAdmin     Role = "admin"
)

type Identity struct {
	Actor             string
	Role              Role
	ApprovalSignature string
}

type Config struct {
	Enabled         bool
	IssuerURL       string
	Audience        string
	RolesClaim      string
	SubjectClaim    string
	ActorClaim      string
	StaticJWTSecret string
}

type Authenticator interface {
	Authenticate(*http.Request) (Identity, error)
}

type contextKey string

const identityKey contextKey = "platform.identity"

var ErrMissingBearerToken = errors.New("missing bearer token")

type JWTAuthenticator struct {
	verifier          *oidc.IDTokenVerifier
	issuerURL         string
	audience          string
	rolesClaim        string
	subjectClaim      string
	actorClaim        string
	staticSecretBytes []byte
}

func NewAuthenticator(ctx context.Context, cfg Config) (Authenticator, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	authenticator := &JWTAuthenticator{
		issuerURL:         cfg.IssuerURL,
		audience:          cfg.Audience,
		rolesClaim:        fallback(cfg.RolesClaim, "role"),
		subjectClaim:      fallback(cfg.SubjectClaim, "sub"),
		actorClaim:        fallback(cfg.ActorClaim, "email"),
		staticSecretBytes: []byte(cfg.StaticJWTSecret),
	}

	if cfg.IssuerURL != "" {
		provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("create oidc provider: %w", err)
		}
		authenticator.verifier = provider.Verifier(&oidc.Config{
			ClientID: cfg.Audience,
		})
	}

	if authenticator.verifier == nil && len(authenticator.staticSecretBytes) == 0 {
		return nil, errors.New("auth enabled but neither OIDC issuer nor static JWT secret configured")
	}

	return authenticator, nil
}

func (a *JWTAuthenticator) Authenticate(r *http.Request) (Identity, error) {
	token, err := bearerToken(r)
	if err != nil {
		return Identity{}, err
	}

	claims := map[string]any{}
	switch {
	case a.verifier != nil:
		idToken, err := a.verifier.Verify(r.Context(), token)
		if err != nil {
			return Identity{}, fmt.Errorf("verify oidc token: %w", err)
		}
		if err := idToken.Claims(&claims); err != nil {
			return Identity{}, fmt.Errorf("decode oidc claims: %w", err)
		}
	default:
		parseOptions := []jwt.ParserOption{}
		if a.audience != "" {
			parseOptions = append(parseOptions, jwt.WithAudience(a.audience))
		}
		if a.issuerURL != "" {
			parseOptions = append(parseOptions, jwt.WithIssuer(a.issuerURL))
		}

		parsed, err := jwt.Parse(token, func(parsed *jwt.Token) (any, error) {
			if !slices.Contains([]string{"HS256", "HS384", "HS512"}, parsed.Method.Alg()) {
				return nil, fmt.Errorf("unexpected signing method %s", parsed.Method.Alg())
			}
			return a.staticSecretBytes, nil
		}, parseOptions...)
		if err != nil {
			return Identity{}, fmt.Errorf("verify jwt token: %w", err)
		}
		mapClaims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			return Identity{}, errors.New("unexpected jwt claims type")
		}
		for key, value := range mapClaims {
			claims[key] = value
		}
	}

	actor := claimString(claims, a.actorClaim)
	if actor == "" {
		actor = claimString(claims, a.subjectClaim)
	}
	role, err := claimRole(claims, a.rolesClaim)
	if err != nil {
		return Identity{}, err
	}
	if actor == "" {
		return Identity{}, errors.New("token missing actor identity claim")
	}

	return Identity{
		Actor:             actor,
		Role:              role,
		ApprovalSignature: strings.TrimSpace(r.Header.Get(HeaderApprovalSignature)),
	}, nil
}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

func FromContext(ctx context.Context) (Identity, bool) {
	value := ctx.Value(identityKey)
	identity, ok := value.(Identity)
	return identity, ok
}

func RoleAllowed(role Role, allowed ...Role) bool {
	for _, candidate := range allowed {
		if role == candidate {
			return true
		}
	}
	return false
}

func ComputeApprovalSignature(secret, requestID, actor, class string) string {
	payload := fmt.Sprintf("approve:%s:%s:%s", requestID, actor, class)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyApprovalSignature(secret, requestID, actor, class, provided string) bool {
	if secret == "" || provided == "" {
		return false
	}
	expected := ComputeApprovalSignature(secret, requestID, actor, class)
	return hmac.Equal([]byte(expected), []byte(provided))
}

func bearerToken(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return "", ErrMissingBearerToken
	}
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", ErrMissingBearerToken
	}
	return strings.TrimSpace(token), nil
}

func claimString(claims map[string]any, key string) string {
	value, ok := claims[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func claimRole(claims map[string]any, key string) (Role, error) {
	value, ok := claims[key]
	if !ok {
		return "", fmt.Errorf("token missing role claim %q", key)
	}

	switch typed := value.(type) {
	case string:
		role := Role(strings.TrimSpace(typed))
		if !isValidRole(role) {
			return "", fmt.Errorf("invalid role %q", role)
		}
		return role, nil
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			role := Role(strings.TrimSpace(text))
			if isValidRole(role) {
				return role, nil
			}
		}
	}

	return "", fmt.Errorf("unable to resolve valid role from claim %q", key)
}

func isValidRole(role Role) bool {
	switch role {
	case RoleViewer, RoleRequester, RoleApprover, RoleAdmin:
		return true
	default:
		return false
	}
}

func fallback(value, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallbackValue
	}
	return value
}
