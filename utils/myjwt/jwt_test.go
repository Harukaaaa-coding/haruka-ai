package myjwt

import (
	"GopherAI/config"
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func jwtTestConfig() *config.Config {
	return &config.Config{
		JwtConfig: config.JwtConfig{
			ExpireDuration: 1,
			Issuer:         "test-issuer",
			Subject:        "test-subject",
			Key:            "S7hQ2mL9vR4xB8nK1pT6wY3cD0fG5jZ2aE7uI4oP",
		},
	}
}

func TestGeneratedTokenCarriesJTI(t *testing.T) {
	conf := jwtTestConfig()
	now := time.Now().UTC()
	token, err := generateToken(42, "12345678901", conf, now, "token-id-for-revocation")
	if err != nil {
		t.Fatalf("generateToken(): %v", err)
	}

	claims, ok := parseToken(token, conf)
	if !ok {
		t.Fatal("parseToken() rejected a token it generated")
	}
	if claims.RegisteredClaims.ID != "token-id-for-revocation" {
		t.Fatalf("jti = %q, want token ID", claims.RegisteredClaims.ID)
	}
	if claims.ID != 42 || claims.Username != "12345678901" {
		t.Fatalf("identity = (%d, %q), want (42, 12345678901)", claims.ID, claims.Username)
	}
}

func TestParseTokenRejectsIssuerOrSubjectMismatch(t *testing.T) {
	conf := jwtTestConfig()
	token, err := generateToken(7, "12345678901", conf, time.Now(), "token-id")
	if err != nil {
		t.Fatalf("generateToken(): %v", err)
	}

	wrongIssuer := jwtTestConfig()
	wrongIssuer.JwtConfig.Issuer = "other-issuer"
	if _, ok := parseToken(token, wrongIssuer); ok {
		t.Fatal("parseToken() accepted token from another issuer")
	}

	wrongSubject := jwtTestConfig()
	wrongSubject.JwtConfig.Subject = "other-subject"
	if _, ok := parseToken(token, wrongSubject); ok {
		t.Fatal("parseToken() accepted token with another subject")
	}
}

func TestParseTokenAcceptsLegacyTokenWithoutJTI(t *testing.T) {
	conf := jwtTestConfig()
	claims := Claims{
		ID:       7,
		Username: "12345678901",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    conf.JwtConfig.Issuer,
			Subject:   conf.JwtConfig.Subject,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(conf.JwtConfig.Key))
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}
	parsed, ok := parseToken(token, conf)
	if !ok {
		t.Fatal("parseToken() rejected an unexpired legacy token")
	}
	if parsed.RegisteredClaims.ID != "" {
		t.Fatalf("legacy jti = %q, want empty", parsed.RegisteredClaims.ID)
	}
}

func TestTokenIDFromReaderUsesCryptographicBytes(t *testing.T) {
	data := bytes.Repeat([]byte{0x5a}, tokenIDBytes)
	got, err := tokenIDFromReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("tokenIDFromReader(): %v", err)
	}
	want := base64.RawURLEncoding.EncodeToString(data)
	if got != want {
		t.Fatalf("token ID = %q, want %q", got, want)
	}
	if len(got) < tokenIDBytes {
		t.Fatalf("token ID is unexpectedly short: %d", len(got))
	}
}

func TestTokenIDFromReaderPropagatesFailure(t *testing.T) {
	_, err := tokenIDFromReader(errorReader{})
	if !errors.Is(err, errRandomFailure) {
		t.Fatalf("tokenIDFromReader() error = %v, want random source error", err)
	}
}

var errRandomFailure = errors.New("random source failed")

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errRandomFailure
}
