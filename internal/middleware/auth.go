package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// contextKey is an unexported type for context keys in this package,
// preventing collisions with keys set by other packages.
type contextKey string

const tenantIDKey contextKey = "tenant_id"

// Require returns HTTP middleware that validates a Bearer JWT signed with
// secret (HS256). On success the tenant_id claim is stored in the request
// context. On failure a 401 JSON error is written and the chain is aborted.
func Require(secret string) func(http.Handler) http.Handler {
	keyFunc := func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			if !strings.HasPrefix(raw, "Bearer ") {
				writeUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			tokenStr := strings.TrimPrefix(raw, "Bearer ")
			token, err := jwt.Parse(tokenStr, keyFunc)
			if err != nil {
				log.Warn().Err(err).Str("path", r.URL.Path).Msg("JWT parse failed")
				writeUnauthorized(w, "invalid token")
				return
			}
			if !token.Valid {
				log.Warn().Str("path", r.URL.Path).Msg("JWT invalid")
				writeUnauthorized(w, "invalid token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeUnauthorized(w, "invalid token claims")
				return
			}

			tenantID, ok := claims["tenant_id"].(string)
			if !ok || tenantID == "" {
				writeUnauthorized(w, "token missing tenant_id claim")
				return
			}

			ctx := context.WithValue(r.Context(), tenantIDKey, tenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetTenantID extracts the tenant ID set by Require middleware.
// Returns an empty string if the value is not present in ctx.
func GetTenantID(ctx context.Context) string {
	v, _ := ctx.Value(tenantIDKey).(string)
	return v
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
