// Package auth closes DISBank's one real security hole (IMPROVEMENT_PLAN
// §1.3): every endpoint used to trust whatever sender_id/user_id the client
// put in the request, so any client could move or read anyone else's money.
// /login now issues a signed JWT; Middleware validates it on every other
// endpoint and injects the authenticated user_id into the request context,
// which handlers use INSTEAD OF any client-supplied id.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"cse512/internal/config"
	"cse512/internal/httpx"

	"github.com/golang-jwt/jwt/v5"
)

// tokenTTL is how long an issued token is valid. Out of scope for this
// change: refresh tokens, revocation/blocklist (see IMPROVEMENT_PLAN §1.3).
const tokenTTL = 24 * time.Hour

var ErrInvalidToken = errors.New("invalid or missing token")

type claims struct {
	UserID int `json:"user_id"`
	jwt.RegisteredClaims
}

// IssueToken signs a JWT (HS256, config.Get().JWTSecret) carrying userID.
func IssueToken(userID int) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
		},
	})
	return token.SignedString([]byte(config.Get().JWTSecret))
}

// ParseToken validates a JWT and returns the user_id it carries.
func ParseToken(tokenString string) (int, error) {
	parsed, err := jwt.ParseWithClaims(tokenString, &claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return []byte(config.Get().JWTSecret), nil
	})
	if err != nil || !parsed.Valid {
		return 0, ErrInvalidToken
	}
	c, ok := parsed.Claims.(*claims)
	if !ok {
		return 0, ErrInvalidToken
	}
	return c.UserID, nil
}

type contextKey struct{}

var userIDKey contextKey

// Middleware requires a valid `Authorization: Bearer <token>` header and
// injects the token's user_id into the request context for the handler to
// read with UserIDFromContext. OPTIONS requests (CORS preflight, which never
// carries the header) pass through untouched; the handler's own OPTIONS
// branch answers them.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		header := r.Header.Get("Authorization")
		tokenString := strings.TrimPrefix(header, "Bearer ")
		if tokenString == "" || tokenString == header {
			writeUnauthorized(w)
			return
		}

		userID, err := ParseToken(tokenString)
		if err != nil {
			writeUnauthorized(w)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, userID)))
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	httpx.Error(w, http.StatusUnauthorized, "Missing or invalid authorization token.")
}

// UserIDFromContext reads the user_id Middleware injected. The ok return is
// false only if called outside Middleware, which handler code should treat
// as a bug, not a request-time condition.
func UserIDFromContext(ctx context.Context) (int, bool) {
	userID, ok := ctx.Value(userIDKey).(int)
	return userID, ok
}
