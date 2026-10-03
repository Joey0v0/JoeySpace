package jwt

import (
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-key-for-unit-test"

func TestGenerateAndParseToken_Valid(t *testing.T) {
	var userID int64 = 12345
	tokenStr, err := GenerateToken(userID, testSecret, 1)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("GenerateToken returned empty token")
	}

	claims, err := ParseToken(tokenStr, testSecret)
	if err != nil {
		t.Fatalf("ParseToken failed: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("expected user_id=%d, got %d", userID, claims.UserID)
	}
	if claims.Issuer != "go-im" {
		t.Errorf("expected issuer=go-im, got %s", claims.Issuer)
	}
}

func TestParseToken_Expired(t *testing.T) {
	// 手动构建一个已过期的 token
	var userID int64 = 99
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwtv5.RegisteredClaims{
			ExpiresAt: jwtv5.NewNumericDate(time.Now().Add(-1 * time.Hour)), // 1 小时前过期
			IssuedAt:  jwtv5.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			Issuer:    "go-im",
		},
	}
	token := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("failed to sign expired token: %v", err)
	}

	_, err = ParseToken(tokenStr, testSecret)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if err != ErrTokenExpired {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestParseToken_Forged(t *testing.T) {
	// 用一个 secret 签名，用另一个 secret 解析
	var userID int64 = 88
	tokenStr, err := GenerateToken(userID, "real-secret", 1)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	_, err = ParseToken(tokenStr, "wrong-secret")
	if err == nil {
		t.Fatal("expected error for forged token, got nil")
	}
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParseToken_MalformedString(t *testing.T) {
	_, err := ParseToken("not-a-jwt-token-at-all", testSecret)
	if err == nil {
		t.Fatal("expected error for malformed token string, got nil")
	}
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParseToken_EmptyString(t *testing.T) {
	_, err := ParseToken("", testSecret)
	if err == nil {
		t.Fatal("expected error for empty token string, got nil")
	}
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestGenerateToken_DifferentUsers(t *testing.T) {
	token1, _ := GenerateToken(1, testSecret, 1)
	token2, _ := GenerateToken(2, testSecret, 1)
	if token1 == token2 {
		t.Error("tokens for different users should be different")
	}
}
