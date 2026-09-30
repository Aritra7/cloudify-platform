package auth

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuthenticatorProtectsEndpointsAndPropagatesRoles(t *testing.T) {
	t.Parallel()
	token := "secret-token"
	digest := sha256.Sum256([]byte(token))
	authenticator, err := NewBearerAuthenticator(fmt.Sprintf(
		`[{"actor":"operator@example.com","token_sha256":"%x","roles":["operator"]}]`, digest,
	))
	if err != nil {
		t.Fatalf("NewBearerAuthenticator: %v", err)
	}
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		principal, err := RequireRole(request.Context(), RoleOperator)
		if err != nil {
			t.Errorf("RequireRole: %v", err)
			return
		}
		_, _ = w.Write([]byte(principal.Actor))
	}))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/plans/id", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/v1/plans/id", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer "+token)
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || authorized.Body.String() != "operator@example.com" {
		t.Fatalf("authorized response = %d %q", authorized.Code, authorized.Body.String())
	}
}

func TestBearerAuthenticatorLeavesProbesPublic(t *testing.T) {
	t.Parallel()
	digest := sha256.Sum256([]byte("token"))
	authenticator, err := NewBearerAuthenticator(fmt.Sprintf(
		`[{"actor":"operator@example.com","token_sha256":"%x","roles":["operator"]}]`, digest,
	))
	if err != nil {
		t.Fatalf("NewBearerAuthenticator: %v", err)
	}
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("probe status = %d", response.Code)
	}
}
