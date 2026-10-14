package oauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

const jwksEndpoint = "https://auth.openai.com/.well-known/jwks.json"

// JWKSValidator verifies OpenAI RS256 ID tokens against the official key set.
type JWKSValidator struct {
	client HTTPDoer
	clock  Clock
}

// NewJWKSValidator wires controlled HTTP and time boundaries for identity validation.
func NewJWKSValidator(client HTTPDoer, clock Clock) (*JWKSValidator, error) {
	if client == nil || clock == nil {
		return nil, &model.Error{Category: model.CategoryInvalidConfig, Operation: "configure id token validator"}
	}
	return &JWKSValidator{client: client, clock: clock}, nil
}

// Validate checks signature, issuer, audience, expiry and the attempt nonce.
func (v *JWKSValidator) Validate(ctx context.Context, token, audience, nonce string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || audience == "" {
		return Identity{}, authError("parse id token")
	}
	var header struct{ Algorithm, KeyID string }
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, authError("decode id token header")
	}
	var wireHeader struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(headerBytes, &wireHeader) != nil || wireHeader.Alg != "RS256" || wireHeader.Kid == "" {
		return Identity{}, authError("validate id token header")
	}
	header.Algorithm, header.KeyID = wireHeader.Alg, wireHeader.Kid
	key, err := v.loadKey(ctx, header.KeyID)
	if err != nil {
		return Identity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, authError("decode id token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return Identity{}, authError("verify id token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, authError("decode id token claims")
	}
	var claims struct {
		Issuer   string          `json:"iss"`
		Subject  string          `json:"sub"`
		Audience json.RawMessage `json:"aud"`
		Expiry   int64           `json:"exp"`
		Nonce    string          `json:"nonce"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Issuer != "https://auth.openai.com" || claims.Subject == "" || claims.Expiry <= v.clock.Now().Unix() || (nonce != "" && claims.Nonce != nonce) || !audienceContains(claims.Audience, audience) {
		return Identity{}, authError("validate id token claims")
	}
	return Identity{Subject: claims.Subject}, nil
}

// loadKey fetches one bounded JWKS document and selects an RSA signing key by ID.
func (v *JWKSValidator) loadKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxModelError("load id token keys", err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, jwksEndpoint, nil)
	response, err := v.client.Do(request)
	if err != nil {
		return nil, oauthBoundaryError(ctx, "load id token keys", err, model.CategoryProviderTransient)
	}
	defer response.Body.Close()
	var document struct {
		Keys []struct {
			KeyID    string `json:"kid"`
			Type     string `json:"kty"`
			Use      string `json:"use"`
			Modulus  string `json:"n"`
			Exponent string `json:"e"`
		} `json:"keys"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&document) != nil {
		return nil, authError("decode id token keys")
	}
	for _, key := range document.Keys {
		if key.KeyID != keyID || key.Type != "RSA" || key.Use != "sig" {
			continue
		}
		modulus, modulusErr := base64.RawURLEncoding.DecodeString(key.Modulus)
		exponent, exponentErr := base64.RawURLEncoding.DecodeString(key.Exponent)
		if modulusErr != nil || exponentErr != nil || len(exponent) == 0 || len(exponent) > 4 {
			break
		}
		exponentValue := 0
		for _, part := range exponent {
			exponentValue = exponentValue<<8 | int(part)
		}
		if exponentValue < 3 {
			break
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponentValue}, nil
	}
	return nil, authError("select id token key")
}

// audienceContains accepts either the OIDC string or array audience shape.
func audienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, value := range multiple {
		if value == expected {
			return true
		}
	}
	return false
}

// WallClock supplies production OAuth time.
type WallClock struct{}

// Now returns current UTC wall time for token expiry validation.
func (WallClock) Now() time.Time { return time.Now().UTC() }
