package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

type Role string

const (
	RolePlanner  Role = "planner"
	RoleApprover Role = "approver"
	RoleOperator Role = "operator"
)

var ErrUnauthenticated = errors.New("authentication required")
var ErrForbidden = errors.New("insufficient role")

type Principal struct {
	Actor string
	Roles map[Role]struct{}
}

type tokenRecord struct {
	Actor       string `json:"actor"`
	TokenSHA256 string `json:"token_sha256"`
	Roles       []Role `json:"roles"`
}

type credential struct {
	principal Principal
	digest    [sha256.Size]byte
}

// BearerAuthenticator verifies pre-hashed service tokens and propagates a
// trusted actor. Plaintext tokens never appear in configuration.
type BearerAuthenticator struct{ credentials []credential }

func NewBearerAuthenticator(configuration string) (*BearerAuthenticator, error) {
	var records []tokenRecord
	if err := json.Unmarshal([]byte(configuration), &records); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("at least one authentication token is required")
	}
	authenticator := &BearerAuthenticator{credentials: make([]credential, 0, len(records))}
	for _, record := range records {
		if strings.TrimSpace(record.Actor) == "" || len(record.Roles) == 0 {
			return nil, errors.New("each token requires an actor and at least one role")
		}
		digestBytes, err := hex.DecodeString(record.TokenSHA256)
		if err != nil || len(digestBytes) != sha256.Size {
			return nil, errors.New("token_sha256 must be a 64-character hexadecimal SHA-256 digest")
		}
		roles := make(map[Role]struct{}, len(record.Roles))
		for _, role := range record.Roles {
			if role != RolePlanner && role != RoleApprover && role != RoleOperator {
				return nil, errors.New("authentication token contains an unsupported role")
			}
			roles[role] = struct{}{}
		}
		var digest [sha256.Size]byte
		copy(digest[:], digestBytes)
		authenticator.credentials = append(authenticator.credentials, credential{
			principal: Principal{Actor: record.Actor, Roles: roles}, digest: digest,
		})
	}
	return authenticator, nil
}

func (authenticator *BearerAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" || request.URL.Path == "/readyz" {
			next.ServeHTTP(w, request)
			return
		}
		value := request.Header.Get("Authorization")
		if !strings.HasPrefix(value, "Bearer ") {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		presented := sha256.Sum256([]byte(strings.TrimPrefix(value, "Bearer ")))
		for _, candidate := range authenticator.credentials {
			if subtle.ConstantTimeCompare(presented[:], candidate.digest[:]) == 1 {
				recordCapturedPrincipal(request.Context(), candidate.principal)
				ctx := context.WithValue(request.Context(), principalKey{}, candidate.principal)
				next.ServeHTTP(w, request.WithContext(ctx))
				return
			}
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

type principalKey struct{}

type principalCaptureKey struct{}

type principalCapture struct {
	mu        sync.RWMutex
	principal Principal
	present   bool
}

// PrincipalFromContext returns the authenticated caller, when present.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

// CapturePrincipal allows an outer request middleware to retrieve the identity
// established by an inner authentication middleware after the handler returns.
func CapturePrincipal(ctx context.Context) (context.Context, func() (Principal, bool)) {
	capture := &principalCapture{}
	return context.WithValue(ctx, principalCaptureKey{}, capture), func() (Principal, bool) {
		capture.mu.RLock()
		defer capture.mu.RUnlock()
		return capture.principal, capture.present
	}
}

func recordCapturedPrincipal(ctx context.Context, principal Principal) {
	capture, ok := ctx.Value(principalCaptureKey{}).(*principalCapture)
	if !ok {
		return
	}
	capture.mu.Lock()
	capture.principal = principal
	capture.present = true
	capture.mu.Unlock()
}

func RequireRole(ctx context.Context, roles ...Role) (Principal, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	for _, role := range roles {
		if _, allowed := principal.Roles[role]; allowed {
			return principal, nil
		}
	}
	return Principal{}, ErrForbidden
}
