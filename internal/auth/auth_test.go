package auth_test

import (
	"testing"
	"time"

	"cse512/internal/auth"
	"cse512/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

func TestIssueAndParseToken_RoundTrips(t *testing.T) {
	token, err := auth.IssueToken(106)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	userID, err := auth.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if userID != 106 {
		t.Errorf("userID = %d, want 106", userID)
	}
}

func TestParseToken_RejectsMalformed(t *testing.T) {
	for _, tokenString := range []string{"", "not-a-jwt", "a.b.c"} {
		if _, err := auth.ParseToken(tokenString); err == nil {
			t.Errorf("ParseToken(%q): expected an error, got nil", tokenString)
		}
	}
}

func TestParseToken_RejectsTamperedSignature(t *testing.T) {
	token, err := auth.IssueToken(106)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	tampered := token[:len(token)-1] + "x"
	if tampered == token {
		t.Fatal("test setup produced an unmodified token")
	}

	if _, err := auth.ParseToken(tampered); err == nil {
		t.Error("expected tampered token to be rejected")
	}
}

func TestParseToken_RejectsExpiredToken(t *testing.T) {
	// Build a token the same way IssueToken does, but with an expiry in the
	// past — auth.claims is unexported, so this uses jwt.MapClaims directly.
	now := time.Now()
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": 106,
		"iat":     now.Add(-2 * time.Hour).Unix(),
		"exp":     now.Add(-time.Hour).Unix(),
	})
	signed, err := expired.SignedString([]byte(config.Get().JWTSecret))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	if _, err := auth.ParseToken(signed); err == nil {
		t.Error("expected expired token to be rejected")
	}
}

func TestParseToken_RejectsWrongSigningMethod(t *testing.T) {
	// alg:"none" tokens must never be accepted — a classic JWT bypass.
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"user_id": 106})
	tokenString, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	if _, err := auth.ParseToken(tokenString); err == nil {
		t.Error("expected alg:none token to be rejected")
	}
}
