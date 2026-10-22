package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func withIntegrationRateLimits(t *testing.T, globalNum int, integrationNum int) {
	t.Helper()
	enable, num, duration := common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration
	inNum, inDuration, redis := common.IntegrationRateLimitNum, common.IntegrationRateLimitDuration, common.RedisEnabled
	t.Cleanup(func() {
		common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = enable, num, duration
		common.IntegrationRateLimitNum, common.IntegrationRateLimitDuration, common.RedisEnabled = inNum, inDuration, redis
	})
	common.GlobalApiRateLimitEnable, common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = true, globalNum, 60
	common.IntegrationRateLimitNum, common.IntegrationRateLimitDuration, common.RedisEnabled = integrationNum, 60, false
}

// The in-memory limiter is package state; every invocation uses fresh keys.
var limiterKeySeq atomic.Int64

func freshLimiterKeys(t *testing.T) (integration string, ip func(int) string) {
	n := limiterKeySeq.Add(1)
	integration = fmt.Sprintf("partner-%d", n)
	t.Setenv("INTEGRATION_CLIENTS", fmt.Sprintf(`{%q:"secret-a"}`, integration))
	return integration, func(i int) string { return fmt.Sprintf("198.51.%d.%d:1000", n%250, i) }
}

func integrationLimitRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api")
	api.Use(GlobalAPIRateLimit())
	api.GET("/status", func(c *gin.Context) { c.Status(http.StatusOK) })
	integrations := api.Group("/integrations")
	integrations.Use(IntegrationAuth(), IntegrationRateLimit())
	integrations.POST("/balance", func(c *gin.Context) { c.Status(http.StatusOK) })
	return router
}

func send(router *gin.Engine, method string, path string, remote string, integration string, secret string) int {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remote
	if secret != "" {
		request.Header.Set("X-Integration-ID", integration)
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Code
}

func TestIntegrationCallsAreNotLimitedBySharedClientIP(t *testing.T) {
	withIntegrationRateLimits(t, 2, 100)
	integration, ip := freshLimiterKeys(t)
	router := integrationLimitRouter()

	for i := 0; i < 5; i++ {
		if code := send(router, http.MethodPost, "/api/integrations/balance", ip(1), integration, "secret-a"); code != http.StatusOK {
			t.Fatalf("integration call %d from a shared address got %d", i+1, code)
		}
	}
	// Public API routes keep the per-IP limit.
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, send(router, http.MethodGet, "/api/status", ip(2), integration, ""))
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("expected the public route to stay limited per IP, got %v", codes)
	}
}

func TestRejectedIntegrationCredentialsStayLimitedPerIP(t *testing.T) {
	withIntegrationRateLimits(t, 2, 100)
	integration, ip := freshLimiterKeys(t)
	router := integrationLimitRouter()

	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, send(router, http.MethodPost, "/api/integrations/balance", ip(3), integration, "wrong"))
	}
	if codes[0] != http.StatusUnauthorized || codes[1] != http.StatusUnauthorized || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("expected 401, 401, 429 for guessed secrets, got %v", codes)
	}
}

func TestIntegrationLimitUsesItsOwnCeiling(t *testing.T) {
	withIntegrationRateLimits(t, 100, 2)
	integration, ip := freshLimiterKeys(t)
	router := integrationLimitRouter()

	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, send(router, http.MethodPost, "/api/integrations/balance", ip(4), integration, "secret-a"))
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("expected the per-integration ceiling to apply, got %v", codes)
	}
}
