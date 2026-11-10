package controller

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Opt-in HTTP peer for Course's test_newapi_cross_service.py. It uses an isolated
// SQLite database, real integration auth/controllers and real wallet settlement.
// Only upstream inference and the root login session are deterministic fixtures.
// This file is never part of the production server.
func TestIntegrationCourseContractHTTPPeer(t *testing.T) {
	address := os.Getenv("TEST_COURSE_CONTRACT_ADDR")
	if address == "" {
		t.Skip("requires the local Course cross-service test client")
	}
	host, _, err := net.SplitHostPort(address)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "0.0.0.0"}, host)
	setupBotcordControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.IntegrationRequest{}, &model.IntegrationRequestReconciliation{}))
	t.Setenv("INTEGRATION_CLIENTS", `{"course":"local-contract-fixture"}`)
	var calls atomic.Int32
	done := make(chan struct{})
	var once sync.Once
	r := gin.New()
	r.Use(sessions.Sessions("contract-test-session", cookie.NewStore([]byte("contract-fixture-signing-key-only"))))
	r.GET("/fixture/health", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.POST("/fixture/login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("id", 999)
		session.Set("username", "contract-root")
		session.Set("role", common.RoleRootUser)
		session.Set("status", common.UserStatusEnabled)
		if session.Save() != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})
	r.POST("/fixture/finish", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
		once.Do(func() { close(done) })
	})
	integration := r.Group("/api/integrations", middleware.IntegrationAuth())
	integration.POST("/provision", IntegrationProvision)
	integration.POST("/requests", IntegrationPrepareRequest)
	integration.POST("/requests/query", IntegrationGetRequest)
	integration.POST("/requests/cancel", IntegrationCancelRequest)
	r.POST("/v1/responses", func(c *gin.Context) {
		var account model.IntegrationAccount
		if model.DB.Where("external_user_id = ?", c.GetHeader("X-Fixture-User")).First(&account).Error != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		info := &relaycommon.RelayInfo{UserId: account.UserId, TokenId: account.TokenId,
			RequestId: "fixture-provider-" + c.GetHeader(service.IntegrationRequestHeader)}
		billing, apiErr := service.NewBillingSession(c, info, 100)
		defer common.CleanupBodyStorage(c)
		if apiErr != nil {
			c.AbortWithStatus(http.StatusConflict)
			return
		}
		if calls.Add(1) == 1 {
			billing.Refund(c)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "fixture upstream failure"})
			return
		}
		if err := billing.Settle(247); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"output": "fixture response"})
	})
	admin := r.Group("/api/user", middleware.AdminAuth())
	admin.GET("/:id/integration-requests", AdminListIntegrationRequests)
	admin.GET("/:id/integration-requests/:request_id", AdminGetIntegrationRequest)
	admin.POST("/:id/integration-requests/:request_id/reconcile", middleware.RootAuth(), AdminReconcileIntegrationRequest)
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(r)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	t.Log("local Course contract peer ready")
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		t.Fatal("Course client did not finish")
	}
	require.EqualValues(t, 2, calls.Load(), "queries and retries must not replay inference")
	var rows []model.IntegrationRequest
	require.NoError(t, model.DB.Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, "settled", rows[0].State)
	require.Equal(t, 0, rows[0].ChargedQuota)
	require.Equal(t, "settled", rows[1].State)
	require.Equal(t, 247, rows[1].ChargedQuota)
	var decisions int64
	require.NoError(t, model.DB.Model(&model.IntegrationRequestReconciliation{}).Count(&decisions).Error)
	require.EqualValues(t, 1, decisions)
	var user model.User
	require.NoError(t, model.DB.First(&user).Error)
	require.EqualValues(t, 1000000-247, user.Quota)
	require.EqualValues(t, 247, user.UsedQuota)
}
