package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash: %s", hash)
	}
	if err := VerifyPassword("correct horse battery staple", hash); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword("wrong", hash); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestJWTBindsVersionsAndSupportsRotation(t *testing.T) {
	old := NewIssuer("old-secret-with-more-than-thirty-two-bytes", "")
	now := time.Unix(1_800_000_000, 0)
	old.now = func() time.Time { return now }
	av, sv := "auth-v1", int64(7)
	token, err := old.Issue(domain.Identity{UserID: "u1", OrgID: "o1", Email: "u@example.com", Role: "admin"}, &av, &sv)
	if err != nil {
		t.Fatal(err)
	}
	rotated := NewIssuer("new-secret-with-more-than-thirty-two-bytes", "old-secret-with-more-than-thirty-two-bytes")
	rotated.now = func() time.Time { return now }
	identity, gotAV, gotSV, err := rotated.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != "u1" || gotAV == nil || *gotAV != av || gotSV == nil || *gotSV != sv {
		t.Fatalf("unexpected claims: %#v %v %v", identity, gotAV, gotSV)
	}
}

func TestRefreshTokensAreRandomAndStoredAsHashes(t *testing.T) {
	one, oneHash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	two, twoHash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if one == two || oneHash == twoHash || oneHash != RefreshHash(one) || strings.Contains(oneHash, one) {
		t.Fatal("invalid refresh token semantics")
	}
}
