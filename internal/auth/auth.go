package auth

import (
	"errors"
	"net/http"
)

var ErrUnauthorized = errors.New("unauthorized")

type Principal struct {
	Subject  string
	Issuer   string
	Audience []string
	Claims   map[string]any
}

type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
}

type NoAuth struct{}

func (NoAuth) Authenticate(r *http.Request) (*Principal, error) {
	return nil, nil
}

