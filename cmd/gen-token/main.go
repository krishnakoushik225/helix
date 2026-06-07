package main

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "error: JWT_SECRET environment variable is not set")
		os.Exit(1)
	}

	tenantID := os.Getenv("TENANT_ID")
	if tenantID == "" {
		fmt.Fprintln(os.Stderr, "error: TENANT_ID environment variable is not set")
		os.Exit(1)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant_id": tenantID,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(24 * time.Hour).Unix(),
	})

	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: sign token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(signed)
}
