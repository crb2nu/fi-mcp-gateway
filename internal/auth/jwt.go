package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc"
	"github.com/golang-jwt/jwt/v4"
)

type Mode string

const (
	ModeNone Mode = "none"
	ModeJWT  Mode = "jwt"
	ModeOIDC Mode = "oidc"
)

type Config struct {
	Mode Mode

	JWKSURL string
	Issuer  string

	// Audiences are acceptable audience values. Empty disables aud checking.
	Audiences []string

	// Required indicates whether auth is required. If false, missing/invalid auth
	// returns a nil principal without error (useful for incremental rollout).
	Required bool

	ClockSkew time.Duration
}

func LoadConfigFromEnv() Config {
	mode := Mode(strings.ToLower(envDefault("FI_MCP_AUTH_MODE", "none")))
	required := strings.EqualFold(envDefault("FI_MCP_AUTH_REQUIRED", "true"), "true")

	audCSV := strings.TrimSpace(os.Getenv("FI_MCP_AUTH_AUDIENCE"))
	auds := splitCSV(audCSV)

	skew := 30 * time.Second
	if v := strings.TrimSpace(os.Getenv("FI_MCP_AUTH_CLOCK_SKEW")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			skew = d
		}
	}

	return Config{
		Mode:      mode,
		JWKSURL:   strings.TrimSpace(os.Getenv("FI_MCP_AUTH_JWKS_URL")),
		Issuer:    strings.TrimSpace(os.Getenv("FI_MCP_AUTH_ISSUER")),
		Audiences: auds,
		Required:  required,
		ClockSkew: skew,
	}
}

func New(ctx context.Context, cfg Config) (Authenticator, error) {
	switch cfg.Mode {
	case "", ModeNone:
		return NoAuth{}, nil
	case ModeJWT, ModeOIDC:
		return NewJWKSAuthenticator(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", cfg.Mode)
	}
}

type JWKSAuthenticator struct {
	cfg    Config
	parser *jwt.Parser
	jwks   *keyfunc.JWKS
}

func NewJWKSAuthenticator(ctx context.Context, cfg Config) (*JWKSAuthenticator, error) {
	if cfg.JWKSURL == "" {
		return nil, fmt.Errorf("missing FI_MCP_AUTH_JWKS_URL")
	}

	jwks, err := keyfunc.Get(cfg.JWKSURL, keyfunc.Options{
		RefreshInterval:   12 * time.Hour,
		RefreshRateLimit:  30 * time.Second,
		RefreshTimeout:    10 * time.Second,
		RefreshUnknownKID: true,
	})
	if err != nil {
		return nil, fmt.Errorf("jwks init: %w", err)
	}

	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}))

	return &JWKSAuthenticator{
		cfg:    cfg,
		parser: parser,
		jwks:   jwks,
	}, nil
}

func (a *JWKSAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		if a.cfg.Required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}

	tokenString := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer"))
	if tokenString == raw {
		// Header was present but not "Bearer ...".
		if a.cfg.Required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		if a.cfg.Required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}

	claims := jwt.MapClaims{}
	token, err := a.parser.ParseWithClaims(tokenString, claims, a.jwks.Keyfunc)
	if err != nil {
		if a.cfg.Required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}
	if token == nil || !token.Valid {
		if a.cfg.Required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}

	if err := validateRegisteredClaims(claims, a.cfg); err != nil {
		if a.cfg.Required {
			return nil, ErrUnauthorized
		}
		return nil, nil
	}

	sub, _ := claims["sub"].(string)
	iss, _ := claims["iss"].(string)

	var aud []string
	if v, ok := claims["aud"]; ok {
		switch vv := v.(type) {
		case string:
			aud = []string{vv}
		case []any:
			for _, it := range vv {
				if s, ok := it.(string); ok {
					aud = append(aud, s)
				}
			}
		}
	}

	return &Principal{
		Subject:  sub,
		Issuer:   iss,
		Audience: aud,
		Claims:   claims,
	}, nil
}

func validateRegisteredClaims(claims jwt.MapClaims, cfg Config) error {
	now := time.Now()

	if cfg.Issuer != "" {
		if iss, _ := claims["iss"].(string); iss != cfg.Issuer {
			return errors.New("issuer mismatch")
		}
	}

	if len(cfg.Audiences) > 0 {
		ok := false
		switch v := claims["aud"].(type) {
		case string:
			ok = contains(cfg.Audiences, v)
		case []any:
			for _, it := range v {
				s, _ := it.(string)
				if s != "" && contains(cfg.Audiences, s) {
					ok = true
					break
				}
			}
		default:
			ok = false
		}
		if !ok {
			return errors.New("audience mismatch")
		}
	}

	if exp, ok := claims["exp"]; ok {
		if ts, ok := asUnixSeconds(exp); ok {
			if now.After(time.Unix(ts, 0).Add(cfg.ClockSkew)) {
				return errors.New("token expired")
			}
		}
	}
	if nbf, ok := claims["nbf"]; ok {
		if ts, ok := asUnixSeconds(nbf); ok {
			if now.Before(time.Unix(ts, 0).Add(-cfg.ClockSkew)) {
				return errors.New("token not yet valid")
			}
		}
	}

	return nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, it := range list {
		if it == s {
			return true
		}
	}
	return false
}

func asUnixSeconds(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case jsonNumber:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

// jsonNumber is the subset of encoding/json.Number we need, without importing encoding/json here.
type jsonNumber interface {
	Int64() (int64, error)
}
