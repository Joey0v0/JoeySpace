package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/yjydist/go-im/internal/config"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
)

const testSecret = "test-middleware-secret"

// setupTestRouter 创建测试用 Gin 路由，挂载 Auth 中间件
func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)

	// 初始化 GlobalConfig 以供 Auth 中间件读取
	config.GlobalConfig = &config.Config{
		JWT: config.JWTConfig{
			Secret:            testSecret,
			AccessExpireHours: 24,
		},
	}

	r := gin.New()
	r.GET("/protected", Auth(), func(c *gin.Context) {
		userID := GetUserID(c)
		c.JSON(http.StatusOK, gin.H{"user_id": userID})
	})
	return r
}

// apiResponse 统一响应结构（用于反序列化断言）
type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data,omitempty"`
}

func TestAuth_MissingHeader(t *testing.T) {
	r := setupTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// errcode.ErrUnAuth = 10002
	if resp.Code != 10002 {
		t.Errorf("expected code 10002 (ErrUnAuth), got %d", resp.Code)
	}
}

func TestAuth_InvalidFormat(t *testing.T) {
	r := setupTestRouter()

	// 缺少 "Bearer " 前缀
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Token some-random-string")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Code != 10002 {
		t.Errorf("expected code 10002 (ErrUnAuth), got %d", resp.Code)
	}
}

func TestAuth_ExpiredToken(t *testing.T) {
	r := setupTestRouter()

	// 构建一个已过期的 token
	claims := pkgjwt.Claims{
		UserID: 42,
		RegisteredClaims: jwtv5.RegisteredClaims{
			ExpiresAt: jwtv5.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwtv5.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			Issuer:    "go-im",
		},
	}
	token := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(testSecret))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// errcode.ErrTokenExpired = 20005
	if resp.Code != 20005 {
		t.Errorf("expected code 20005 (ErrTokenExpired), got %d", resp.Code)
	}
}

func TestAuth_ForgedToken(t *testing.T) {
	r := setupTestRouter()

	// 用不同 secret 签名
	tokenStr, _ := pkgjwt.GenerateToken(42, "wrong-secret-key", 1)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// errcode.ErrTokenInvalid = 20006
	if resp.Code != 20006 {
		t.Errorf("expected code 20006 (ErrTokenInvalid), got %d", resp.Code)
	}
}

func TestAuth_ValidToken(t *testing.T) {
	r := setupTestRouter()

	var expectedUserID int64 = 12345
	tokenStr, err := pkgjwt.GenerateToken(expectedUserID, testSecret, 1)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	gotUserID := int64(body["user_id"].(float64))
	if gotUserID != expectedUserID {
		t.Errorf("expected user_id=%d, got %d", expectedUserID, gotUserID)
	}
}
