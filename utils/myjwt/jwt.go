package myjwt

import (
	"GopherAI/config"
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const tokenIDBytes = 32

type Claims struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// Identity is the authenticated token identity. TokenID is the JWT jti claim
// and is deliberately available to middleware so a future revocation store can
// invalidate a single token without changing the public login response.
type Identity struct {
	UserID   int64
	Username string
	TokenID  string
}

func GenerateToken(id int64, username string) (string, error) {
	conf := config.GetConfig()
	if conf == nil {
		return "", fmt.Errorf("JWT configuration is unavailable")
	}
	tokenID, err := newTokenID()
	if err != nil {
		return "", fmt.Errorf("generate JWT token ID: %w", err)
	}
	return generateToken(id, username, conf, time.Now(), tokenID)
}

func generateToken(id int64, username string, conf *config.Config, now time.Time, tokenID string) (string, error) {
	if conf == nil {
		return "", fmt.Errorf("JWT configuration is unavailable")
	}
	if id <= 0 || strings.TrimSpace(username) == "" {
		return "", fmt.Errorf("JWT subject is invalid")
	}
	if strings.TrimSpace(conf.JwtConfig.Key) == "" {
		return "", fmt.Errorf("JWT signing key is unavailable")
	}
	if strings.TrimSpace(tokenID) == "" {
		return "", fmt.Errorf("JWT token ID is unavailable")
	}

	claims := Claims{
		ID:       id,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(conf.JwtConfig.ExpireDuration) * time.Hour)),
			Issuer:    conf.JwtConfig.Issuer,
			Subject:   conf.JwtConfig.Subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        tokenID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(conf.JwtConfig.Key))
}

// ParseToken preserves the original username-only API for existing callers.
func ParseToken(token string) (string, bool) {
	identity, ok := ParseTokenIdentity(token)
	if !ok {
		return "", false
	}
	return identity.Username, true
}

// ParseTokenIdentity validates a token and exposes its stable jti claim for
// middleware and future per-token invalidation.
func ParseTokenIdentity(token string) (Identity, bool) {
	conf := config.GetConfig()
	if conf == nil {
		return Identity{}, false
	}
	claims, ok := parseToken(token, conf)
	if !ok {
		return Identity{}, false
	}
	return Identity{
		UserID:   claims.ID,
		Username: claims.Username,
		TokenID:  claims.RegisteredClaims.ID,
	}, true
}

func parseToken(token string, conf *config.Config) (*Claims, bool) {
	if conf == nil || strings.TrimSpace(conf.JwtConfig.Key) == "" {
		return nil, false
	}
	claims := new(Claims)
	parsed, err := jwt.ParseWithClaims(token, claims, func(parsed *jwt.Token) (interface{}, error) {
		if parsed.Method == nil || parsed.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			method := "<nil>"
			if parsed.Method != nil {
				method = parsed.Method.Alg()
			}
			return nil, fmt.Errorf("unexpected JWT signing method: %s", method)
		}
		return []byte(conf.JwtConfig.Key), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || parsed == nil || !parsed.Valid || claims.ID <= 0 || strings.TrimSpace(claims.Username) == "" {
		return nil, false
	}
	if claims.Issuer != conf.JwtConfig.Issuer || claims.Subject != conf.JwtConfig.Subject {
		return nil, false
	}
	return claims, true
}

func newTokenID() (string, error) {
	return tokenIDFromReader(cryptorand.Reader)
}

func tokenIDFromReader(reader io.Reader) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("secure random source is unavailable")
	}
	bytes := make([]byte, tokenIDBytes)
	if _, err := io.ReadFull(reader, bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
