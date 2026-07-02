package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func TestJWKSAuthenticator_ValidToken(t *testing.T) {
	t.Parallel()

	priv, jwksURL := startJWKS(t)

	cfg := Config{
		Mode:      ModeJWT,
		JWKSURL:   jwksURL,
		Issuer:    "https://issuer.example",
		Audiences: []string{"mcp"},
		Required:  true,
		ClockSkew: 0,
	}

	a, err := NewJWKSAuthenticator(context.TODO(), cfg)
	if err != nil {
		t.Fatalf("NewJWKSAuthenticator: %v", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-123",
		"iss": cfg.Issuer,
		"aud": "mcp",
		"exp": time.Now().Add(2 * time.Minute).Unix(),
	})
	token.Header["kid"] = "test"
	raw, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example/ws", nil)
	req.Header.Set("Authorization", "Bearer "+raw)

	p, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p == nil || p.Subject != "user-123" {
		t.Fatalf("principal: %#v", p)
	}
}

func TestJWKSAuthenticator_MissingToken(t *testing.T) {
	t.Parallel()

	_, jwksURL := startJWKS(t)

	cfg := Config{
		Mode:     ModeJWT,
		JWKSURL:  jwksURL,
		Required: true,
	}

	a, err := NewJWKSAuthenticator(context.TODO(), cfg)
	if err != nil {
		t.Fatalf("NewJWKSAuthenticator: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example/ws", nil)
	_, err = a.Authenticate(req)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestJWKSAuthenticator_IssuerMismatch(t *testing.T) {
	t.Parallel()

	priv, jwksURL := startJWKS(t)

	cfg := Config{
		Mode:      ModeJWT,
		JWKSURL:   jwksURL,
		Issuer:    "https://issuer.example",
		Audiences: []string{"mcp"},
		Required:  true,
	}

	a, err := NewJWKSAuthenticator(context.TODO(), cfg)
	if err != nil {
		t.Fatalf("NewJWKSAuthenticator: %v", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-123",
		"iss": "https://wrong-issuer.example",
		"aud": "mcp",
		"exp": time.Now().Add(2 * time.Minute).Unix(),
	})
	token.Header["kid"] = "test"
	raw, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example/ws", nil)
	req.Header.Set("Authorization", "Bearer "+raw)

	_, err = a.Authenticate(req)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func startJWKS(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	n := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01}) // 65537

	doc := map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"kid": "test",
				"use": "sig",
				"alg": "RS256",
				"n":   n,
				"e":   e,
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(ts.Close)

	return priv, ts.URL
}
