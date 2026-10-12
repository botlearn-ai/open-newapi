package service

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupIntegrationRequestTest(t *testing.T) (*gorm.DB, dto.IntegrationRequestInput, *relaycommon.RelayInfo) {
	t.Helper()
	oldDB, oldLog, oldRedis := model.DB, common.LogConsumeEnabled, common.RedisEnabled
	var dialect gorm.Dialector = sqlite.Open(":memory:")
	if dsn := os.Getenv("TEST_INTEGRATION_POSTGRES_DSN"); dsn != "" {
		admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		scope := fmt.Sprintf("credit_test_%d", time.Now().UnixNano())
		require.NoError(t, admin.Exec("CREATE SCHEMA "+scope).Error)
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		query := u.Query()
		query.Set("search_path", scope)
		u.RawQuery = query.Encode()
		dialect = postgres.Open(u.String())
		t.Cleanup(func() {
			require.NoError(t, admin.Exec("DROP SCHEMA "+scope+" CASCADE").Error)
			conn, err := admin.DB()
			require.NoError(t, err)
			_ = conn.Close()
		})
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB, common.LogConsumeEnabled, common.RedisEnabled = db, false, false
	t.Cleanup(func() {
		model.DB, common.LogConsumeEnabled, common.RedisEnabled = oldDB, oldLog, oldRedis
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.IntegrationAccount{}, &model.IntegrationRequest{}))
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "credit-test", Status: common.UserStatusEnabled, Quota: 1000}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 1, UserId: 1, Key: "test-only", Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1}).Error)
	_, key, err := integrationExternalIdentity("learner")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.IntegrationAccount{Id: 1, IntegrationId: "course", ExternalUserId: "learner", ExternalUserKey: key, UserId: 1, TokenId: 1, BillingProtocol: IntegrationBillingProtocol}).Error)
	return db, dto.IntegrationRequestInput{ExternalUserId: "learner", RequestKey: "attempt-1", Path: "/v1/chat/completions", BodySHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(`{"model":"test"}`))), MaxQuota: 300, QuotaPerUsd: common.QuotaPerUnit}, &relaycommon.RelayInfo{UserId: 1, TokenId: 1, RequestId: "provider-1"}
}

func TestIntegrationRequestConcurrentSettlement(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	if os.Getenv("TEST_INTEGRATION_POSTGRES_DSN") == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(8)
	request, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- settleIntegrationRequest(request.Id, 100) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	assertIntegrationBalance(t, db, 900, 100)
}

func TestIntegrationRequestConcurrentPrepareAndCancel(t *testing.T) {
	db, req, _ := setupIntegrationRequestTest(t)
	if os.Getenv("TEST_INTEGRATION_POSTGRES_DSN") == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(8)
	for i := 0; i < 12; i++ {
		req.RequestKey = fmt.Sprintf("race-%d", i)
		start, results := make(chan struct{}), make(chan error, 2)
		go func() { <-start; _, err := PrepareIntegrationRequest("course", req); results <- err }()
		go func() { <-start; _, err := CancelIntegrationRequest("course", req); results <- err }()
		close(start)
		require.NoError(t, <-results)
		require.NoError(t, <-results)
		final, err := GetIntegrationRequest("course", req)
		require.NoError(t, err)
		require.Equal(t, "cancelled", final.State)
	}
	assertIntegrationBalance(t, db, 1000, 0)
}

func TestIntegrationRequestConcurrentClaimAndCancel(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	if os.Getenv("TEST_INTEGRATION_POSTGRES_DSN") == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(8)
	_, err = PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", req.Path, strings.NewReader(`{"model":"test"}`))
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	defer common.CleanupBodyStorage(c)
	start, claimed, cancelled := make(chan struct{}), make(chan bool, 1), make(chan error, 1)
	go func() { <-start; _, err := claimIntegrationBilling(c, info, 100); claimed <- err == nil }()
	go func() { <-start; _, err := CancelIntegrationRequest("course", req); cancelled <- err }()
	close(start)
	claimSucceeded, cancelErr := <-claimed, <-cancelled
	final, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	if claimSucceeded {
		require.ErrorIs(t, cancelErr, ErrIntegrationRequestConflict)
		require.Equal(t, "running", final.State)
		assertIntegrationBalance(t, db, 700, 0)
	} else {
		require.NoError(t, cancelErr)
		require.Equal(t, "cancelled", final.State)
		assertIntegrationBalance(t, db, 1000, 0)
	}
}

func claimTestRequest(t *testing.T, req dto.IntegrationRequestInput, info *relaycommon.RelayInfo) *BillingSession {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", req.Path, strings.NewReader(`{"model":"test"}`))
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	session, apiErr := claimIntegrationBilling(c, info, 100)
	require.Nil(t, apiErr)
	info.Billing = session
	return session
}

func assertIntegrationBalance(t *testing.T, db *gorm.DB, balance, used int) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, db.First(&user, 1).Error)
	require.NoError(t, db.First(&token, 1).Error)
	require.Equal(t, balance, user.Quota)
	require.Equal(t, balance, token.RemainQuota)
	require.Equal(t, used, user.UsedQuota)
	require.Equal(t, used, token.UsedQuota)
}

func TestIntegrationRequestSettlementAndReplay(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	again, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, first.Id, again.Id)
	changed := req
	changed.MaxQuota++
	_, err = PrepareIntegrationRequest("course", changed)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	session := claimTestRequest(t, req, info)
	assertIntegrationBalance(t, db, 700, 0)
	require.NoError(t, session.Settle(120))
	require.NoError(t, session.Settle(120))
	session.Refund(nil)
	assertIntegrationBalance(t, db, 880, 120)
	require.ErrorIs(t, session.Settle(121), ErrIdempotencyConflict)
	final, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "settled", final.State)
	require.Equal(t, 120, final.ActualQuota)
	require.Equal(t, info.RequestId, final.ProviderRequestId)
	_, err = GetIntegrationRequest("other", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
}

func TestIntegrationRequestUnknownKeepsHoldAndOverrunEvidence(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	session := claimTestRequest(t, req, info)
	session.Refund(nil)
	assertIntegrationBalance(t, db, 700, 0)
	pending, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "running", pending.State)
	_, err = CancelIntegrationRequest("course", req)
	require.ErrorIs(t, err, ErrIntegrationRequestConflict)
	require.NoError(t, session.Settle(450))
	final, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "disputed", final.State)
	require.Equal(t, 450, final.ActualQuota)
	assertIntegrationBalance(t, db, 700, 0)
	req.RequestKey = "next-attempt"
	_, err = PrepareIntegrationRequest("course", req)
	require.ErrorIs(t, err, ErrIntegrationRequestConflict)
}

func TestIntegrationRequestRequiresExactMoneyUnit(t *testing.T) {
	_, req, _ := setupIntegrationRequestTest(t)
	original := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = original })
	common.QuotaPerUnit = 7
	_, err := PrepareIntegrationRequest("course", req)
	require.ErrorIs(t, err, ErrIntegrationRequestConflict)
}

func TestIntegrationRequestCancelAndBinding(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	req.QuotaPerUsd = common.QuotaPerUnit
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", req.Path, strings.NewReader(`{"model":"different"}`))
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	_, apiErr := claimIntegrationBilling(c, info, 100)
	require.NotNil(t, apiErr)
	common.CleanupBodyStorage(c)
	assertIntegrationBalance(t, db, 1000, 0)
	cancelled, err := CancelIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	_, err = CancelIntegrationRequest("course", req)
	require.NoError(t, err)
	c.Request = httptest.NewRequest("POST", req.Path, strings.NewReader(`{"model":"test"}`))
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	_, apiErr = claimIntegrationBilling(c, info, 100)
	require.NotNil(t, apiErr)
	common.CleanupBodyStorage(c)
	assertIntegrationBalance(t, db, 1000, 0)
}

func TestIntegrationRequestSettlementAtomicWithToken(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	session := claimTestRequest(t, req, info)
	require.NoError(t, db.Delete(&model.Token{}, 1).Error)
	require.Error(t, session.Settle(100))
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, 700, user.Quota)
	var request model.IntegrationRequest
	require.NoError(t, db.First(&request, session.integrationRequest.Id).Error)
	require.Equal(t, "settling", request.State)
	require.Equal(t, 100, request.ActualQuota)
	session.Refund(nil)
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, 700, user.Quota)
}

func TestIntegrationRequestRouteCannotBypassBudget(t *testing.T) {
	_, req, _ := setupIntegrationRequestTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", req.Path, nil)
	c.Set("token_id", 1)
	require.ErrorIs(t, ValidateIntegrationBillingRoute(c, 1), ErrIntegrationRequestConflict)
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	require.NoError(t, ValidateIntegrationBillingRoute(c, 1))
	c.Set("token_id", 2)
	require.ErrorIs(t, ValidateIntegrationBillingRoute(c, 1), ErrIntegrationRequestConflict)
	c.Set("token_id", 1)
	c.Request.URL.Path = "/v1/images/generations"
	require.ErrorIs(t, ValidateIntegrationBillingRoute(c, 1), ErrIntegrationRequestConflict)
	require.ErrorIs(t, ValidateIntegrationBillingRoute(c, 2), ErrIntegrationRequestConflict)
}

func TestIntegrationRequestClaimAtomicAndSingleUse(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 1).Update("remain_quota", 20).Error)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", req.Path, strings.NewReader(`{"model":"test"}`))
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	_, apiErr := claimIntegrationBilling(c, info, 100)
	require.NotNil(t, apiErr)
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, 1000, user.Quota)
	request, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "ready", request.State)
	require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 1).Update("remain_quota", 1000).Error)
	session := claimTestRequest(t, req, info)
	_, apiErr = claimIntegrationBilling(c, info, 100)
	require.NotNil(t, apiErr)
	common.CleanupBodyStorage(c)
	require.NoError(t, session.Settle(0))
	assertIntegrationBalance(t, db, 1000, 0)
}
