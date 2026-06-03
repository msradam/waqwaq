// Package auth maps bearer tokens to named principals. When no tokens file is
// configured the wiki runs in open mode: every caller is an anonymous, trusted
// principal, which keeps zero-config local use working.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type Principal struct {
	Name    string
	Trusted bool
	Anon    bool
}

type tokenEntry struct {
	Token   string `json:"token"`
	Name    string `json:"name"`
	Trusted bool   `json:"trusted"`
}

type tokenFile struct {
	Tokens []tokenEntry `json:"tokens"`
}

type Registry struct {
	byToken map[string]Principal
}

// Load reads a tokens file. A missing file yields a disabled registry, meaning
// open mode (no authentication required).
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f tokenFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	r := &Registry{byToken: make(map[string]Principal, len(f.Tokens))}
	for _, t := range f.Tokens {
		name := sanitizeName(t.Name)
		if t.Token == "" || name == "" {
			continue
		}
		r.byToken[t.Token] = Principal{Name: name, Trusted: t.Trusted}
	}
	return r, nil
}

// sanitizeName strips characters that could break a git --author "Name <email>"
// argument. Token names are operator-configured, but this keeps attribution safe.
func sanitizeName(name string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, name))
}

// Enabled reports whether any tokens are configured. When false the server runs
// in open mode and does not require authentication.
func (r *Registry) Enabled() bool { return len(r.byToken) > 0 }

// Anonymous is the principal used in open mode. It is trusted, so writes commit
// directly unless review is forced.
func Anonymous() Principal { return Principal{Name: "anonymous", Trusted: true, Anon: true} }

// Resolve identifies the principal for an Authorization header value. The second
// result is false when authentication is required but the token is missing or
// unknown.
func (r *Registry) Resolve(authHeader string) (Principal, bool) {
	if !r.Enabled() {
		return Anonymous(), true
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if token == "" {
		return Principal{}, false
	}
	p, ok := r.byToken[token]
	if !ok {
		return Principal{}, false
	}
	return p, true
}
