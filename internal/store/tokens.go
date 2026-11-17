package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Token is an API credential. The secret is shown once, when it is created;
// only a salted hash is stored.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Errors from token checks.
var (
	ErrTokenInvalid = errors.New("token is not valid")
	ErrTokenExpired = errors.New("token has expired or was revoked")
)

var (
	tokenNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	scopePattern     = regexp.MustCompile(`^(read|write):(\*|[a-z0-9_]+\*?)$`)
)

// Allows reports whether the token may read or write records of a kind.
// "write" implies "read". A scope pattern is a kind name, "name*" or "*".
func (t Token) Allows(verb, kind string) bool {
	for _, sc := range t.Scopes {
		parts := strings.SplitN(sc, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] != verb && !(verb == "read" && parts[0] == "write") {
			continue
		}
		if scopeMatches(parts[1], kind) {
			return true
		}
	}
	return false
}

// CoversAllKinds reports whether the token has the verb on every kind.
func (t Token) CoversAllKinds(verb string) bool {
	for _, sc := range t.Scopes {
		if sc == verb+":*" || (verb == "read" && sc == "write:*") {
			return true
		}
	}
	return false
}

func scopeMatches(pattern, kind string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(kind, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == kind
}

func tokenHash(salt, secret string) string {
	sum := sha256.Sum256([]byte(salt + ":" + secret))
	return hex.EncodeToString(sum[:])
}

// CreateToken makes a token and returns its secret, which is not recoverable.
func (s *Store) CreateToken(ctx context.Context, name string, scopes []string, ttl time.Duration) (string, Token, error) {
	if !tokenNamePattern.MatchString(name) {
		return "", Token{}, errors.New("token name must be lowercase letters, digits, hyphens or underscores (up to 40)")
	}
	if len(scopes) == 0 {
		return "", Token{}, errors.New("at least one scope is required, for example read:* or write:dock_*")
	}
	for _, sc := range scopes {
		if !scopePattern.MatchString(sc) {
			return "", Token{}, fmt.Errorf("invalid scope %q: use read:<kind> or write:<kind>, where <kind> is a name, name* or *", sc)
		}
	}
	id, err := newID()
	if err != nil {
		return "", Token{}, err
	}
	id = id[:16]
	var raw, saltRaw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", Token{}, err
	}
	if _, err = rand.Read(saltRaw[:16]); err != nil {
		return "", Token{}, err
	}
	salt := hex.EncodeToString(saltRaw[:16])
	secret := "lh_" + id + "_" + base64.RawURLEncoding.EncodeToString(raw[:])
	now := time.Now().UTC()
	t := Token{ID: id, Name: name, Scopes: scopes, CreatedAt: now}
	var expires any
	if ttl > 0 {
		e := now.Add(ttl)
		t.ExpiresAt = &e
		expires = e.Format(time.RFC3339Nano)
	}
	scopesJSON, _ := json.Marshal(scopes)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO api_tokens(id,name,hash,salt,scopes_json,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`,
		id, name, tokenHash(salt, secret), salt, string(scopesJSON), now.Format(time.RFC3339Nano), expires); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", Token{}, fmt.Errorf("a token named %q already exists; revoke it or rotate it", name)
		}
		return "", Token{}, err
	}
	return secret, t, nil
}

func scanToken(r rowScanner) (Token, string, string, error) {
	var t Token
	var scopes, created, hash, salt string
	var expires, used, revoked sql.NullString
	if err := r.Scan(&t.ID, &t.Name, &hash, &salt, &scopes, &created, &expires, &used, &revoked); err != nil {
		return Token{}, "", "", err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	parse := func(n sql.NullString) *time.Time {
		if !n.Valid || n.String == "" {
			return nil
		}
		v, err := time.Parse(time.RFC3339Nano, n.String)
		if err != nil {
			return nil
		}
		return &v
	}
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = parse(expires), parse(used), parse(revoked)
	return t, hash, salt, nil
}

const tokenColumns = `id,name,hash,salt,scopes_json,created_at,expires_at,last_used_at,revoked_at`

// VerifyToken checks a presented secret and returns its token. Failures never
// say which part was wrong.
func (s *Store) VerifyToken(ctx context.Context, secret string) (Token, error) {
	parts := strings.SplitN(secret, "_", 3)
	if len(parts) != 3 || parts[0] != "lh" || len(parts[1]) != 16 {
		return Token{}, ErrTokenInvalid
	}
	t, hash, salt, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE id=?`, parts[1]))
	if err != nil {
		return Token{}, ErrTokenInvalid
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(tokenHash(salt, secret))) != 1 {
		return Token{}, ErrTokenInvalid
	}
	now := time.Now().UTC()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && now.After(*t.ExpiresAt)) {
		return Token{}, ErrTokenExpired
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, now.Format(time.RFC3339Nano), t.ID)
		t.LastUsedAt = &now
	}
	return t, nil
}

// ListTokens returns every token, newest first. Secrets are never included.
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, _, _, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken disables a token by ID or name.
func (s *Store) RevokeToken(ctx context.Context, idOrName string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at=? WHERE (id=? OR name=?) AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), idOrName, idOrName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no active token matches %q", idOrName)
	}
	return nil
}

// RotateToken revokes a token and issues a new one with the same name, scopes
// and lifetime.
func (s *Store) RotateToken(ctx context.Context, idOrName string) (string, Token, error) {
	t, _, _, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE (id=? OR name=?) AND revoked_at IS NULL`, idOrName, idOrName))
	if err != nil {
		return "", Token{}, fmt.Errorf("no active token matches %q", idOrName)
	}
	var ttl time.Duration
	if t.ExpiresAt != nil {
		if ttl = time.Until(*t.ExpiresAt); ttl < time.Hour {
			ttl = 365 * 24 * time.Hour
		}
	}
	if err := s.RevokeToken(ctx, t.ID); err != nil {
		return "", Token{}, err
	}
	// The revoked row keeps the name unique, so rename it before reuse.
	if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET name=? WHERE id=?`, t.Name+"-revoked-"+t.ID[:6], t.ID); err != nil {
		return "", Token{}, err
	}
	return s.CreateToken(ctx, t.Name, t.Scopes, ttl)
}

// ---- Idempotency ----

// ErrIdempotencyConflict means a key was reused with a different request.
var ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")

// IdempotentLookup returns the stored response for a key used within 24 hours.
func (s *Store) IdempotentLookup(ctx context.Context, key, requestHash string) ([]byte, bool, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM idempotency WHERE at<?`, cutoff)
	var stored, response string
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,response_json FROM idempotency WHERE key=?`, key).Scan(&stored, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored != requestHash {
		return nil, false, ErrIdempotencyConflict
	}
	return []byte(response), true, nil
}

// IdempotentStore remembers a response under a key.
func (s *Store) IdempotentStore(ctx context.Context, key, requestHash string, response []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO idempotency(key,request_hash,response_json,at) VALUES(?,?,?,?)`, key, requestHash, string(response), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
