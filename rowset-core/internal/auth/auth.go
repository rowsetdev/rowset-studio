package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"golang.org/x/crypto/argon2"
)

const (
	issuer   = "rowset"
	audience = "rowset-api"
)

var ErrBadCredentials = errors.New("bad credentials")

type Claims struct {
	Subject        string  `json:"sub"`
	Org            string  `json:"org"`
	Email          string  `json:"email"`
	Role           string  `json:"role"`
	ExpiresAt      int64   `json:"exp"`
	IssuedAt       int64   `json:"iat"`
	Issuer         string  `json:"iss"`
	Audience       string  `json:"aud"`
	AuthVersion    *string `json:"av,omitempty"`
	SessionVersion *int64  `json:"sv,omitempty"`
}

type Issuer struct {
	secret   []byte
	previous []byte
	ttl      time.Duration
	now      func() time.Time
}

func NewIssuer(secret string, previous string) *Issuer {
	return &Issuer{secret: []byte(secret), previous: []byte(previous), ttl: 15 * time.Minute, now: time.Now}
}

func (i *Issuer) Issue(identity domain.Identity, authVersion *string, sessionVersion *int64) (string, error) {
	now := i.now().UTC()
	claims := Claims{Subject: identity.UserID, Org: identity.OrgID, Email: identity.Email, Role: identity.Role,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(i.ttl).Unix(), Issuer: issuer, Audience: audience,
		AuthVersion: authVersion, SessionVersion: sessionVersion}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := rawURL.EncodeToString(header) + "." + rawURL.EncodeToString(payload)
	return unsigned + "." + rawURL.EncodeToString(sign(unsigned, i.secret)), nil
}

func (i *Issuer) Verify(token string) (domain.Identity, *string, *int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return domain.Identity{}, nil, nil, errors.New("invalid token")
	}
	unsigned := parts[0] + "." + parts[1]
	sig, err := rawURL.DecodeString(parts[2])
	if err != nil {
		return domain.Identity{}, nil, nil, errors.New("invalid token")
	}
	valid := hmac.Equal(sig, sign(unsigned, i.secret))
	if !valid && len(i.previous) > 0 {
		valid = hmac.Equal(sig, sign(unsigned, i.previous))
	}
	if !valid {
		return domain.Identity{}, nil, nil, errors.New("invalid signature")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	headerBytes, err := rawURL.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return domain.Identity{}, nil, nil, errors.New("invalid header")
	}
	payload, err := rawURL.DecodeString(parts[1])
	if err != nil {
		return domain.Identity{}, nil, nil, errors.New("invalid token")
	}
	var claims Claims
	if json.Unmarshal(payload, &claims) != nil {
		return domain.Identity{}, nil, nil, errors.New("invalid claims")
	}
	now := i.now().Unix()
	if claims.Issuer != issuer || claims.Audience != audience || claims.ExpiresAt <= now || claims.IssuedAt > now+60 {
		return domain.Identity{}, nil, nil, errors.New("invalid claims")
	}
	return domain.Identity{UserID: claims.Subject, OrgID: claims.Org, Email: claims.Email, Role: claims.Role}, claims.AuthVersion, claims.SessionVersion, nil
}

func sign(unsigned string, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(unsigned))
	return h.Sum(nil)
}

var rawURL = base64.RawURLEncoding

// HashPassword uses the same Argon2id defaults emitted by Rust's argon2 0.5
// crate so either backend can authenticate hashes created by the other.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const memory, iterations, parallelism, keyLength = uint32(19456), uint32(2), uint8(1), uint32(32)
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return ErrBadCredentials
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return ErrBadCredentials
	}
	memory, err1 := parseParam(params[0], "m=")
	iterations, err2 := parseParam(params[1], "t=")
	parallel, err3 := parseParam(params[2], "p=")
	salt, err4 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err5 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || memory == 0 || iterations == 0 || parallel == 0 || len(want) == 0 {
		return ErrBadCredentials
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallel), uint32(len(want)))
	if !hmac.Equal(got, want) {
		return ErrBadCredentials
	}
	return nil
}

func parseParam(value, prefix string) (uint64, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, ErrBadCredentials
	}
	return strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 32)
}

func PasswordAuthVersion(passwordHash string) string {
	h := sha256.Sum256([]byte("rowset-auth-version-v1:" + passwordHash))
	return hex.EncodeToString(h[:])
}

func NewRefreshToken() (token, hash string, err error) {
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(random)
	return token, RefreshHash(token), nil
}

func RefreshHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
