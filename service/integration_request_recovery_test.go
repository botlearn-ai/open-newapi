package service

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func initIntegrationTokenColumns(t *testing.T) {
	t.Helper()
	oldDB, oldPath, oldMaster := model.DB, common.SQLitePath, common.IsMasterNode
	dsn := os.Getenv("TEST_INTEGRATION_POSTGRES_DSN")
	if dsn == "" {
		dsn = "local"
	}
	t.Setenv("SQL_DSN", dsn)
	common.SQLitePath, common.IsMasterNode = filepath.Join(t.TempDir(), "column-init.db"), false
	require.NoError(t, model.InitDB())
	conn, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	model.DB, common.SQLitePath, common.IsMasterNode = oldDB, oldPath, oldMaster
}

func TestIntegrationRequestRefundRestoresExhaustedToken(t *testing.T) {
	initIntegrationTokenColumns(t)
	db, req, info := setupIntegrationRequestTest(t)
	req.MaxQuota = 1000
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	session := claimTestRequest(t, req, info)
	_, err = model.ValidateUserToken("test-only")
	require.ErrorIs(t, err, model.ErrTokenInvalid)
	require.NoError(t, session.Settle(100))
	var token model.Token
	require.NoError(t, db.First(&token, 1).Error)
	t.Logf("remaining=%d status=%d expected_enabled=%d", token.RemainQuota, token.Status, common.TokenStatusEnabled)
	_, err = model.ValidateUserToken("test-only")
	require.NoError(t, err, "refunded balance must be usable without another top-up")
}

func TestIntegrationRequestConversionUsesFrozenUnit(t *testing.T) {
	_, req, info := setupIntegrationRequestTest(t)
	original := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = original })
	common.QuotaPerUnit = 500000
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	session := claimTestRequest(t, req, info)
	info.PriceData.UsePrice = true
	info.PriceData.ModelPrice = 0.0001
	info.PriceData.GroupRatioInfo.GroupRatio = 1
	common.QuotaPerUnit = 1000000
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	summary := calculateTextQuotaSummary(c, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	require.NoError(t, session.Settle(summary.Quota))
	final, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	t.Logf("price_usd=0.0001 charged=%d snapshot_unit=%v reported_usd=%v", final.ActualQuota, final.QuotaPerUsd, float64(final.ActualQuota)/final.QuotaPerUsd)
	require.Equal(t, 0.0001, float64(final.ActualQuota)/final.QuotaPerUsd)
}

func TestIntegrationRequestToolAndAudioConversionUsesFrozenUnit(t *testing.T) {
	_, req, info := setupIntegrationRequestTest(t)
	unit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = unit })
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info)
	info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{
		BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
			dto.BuildInToolWebSearchPreview: {CallCount: 1},
			dto.BuildInToolFileSearch:       {CallCount: 2},
		},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	summary := textQuotaSummary{ModelName: "test", GroupRatio: 1}
	before := calculateTextToolCallSurcharge(c, info, &summary)
	require.True(t, before.IsPositive())
	audio := QuotaInfo{UsePrice: true, ModelPrice: 0.0001, GroupRatio: 1}
	audioBefore := calculateAudioQuota(audio, billingQuotaPerUnit(info))
	common.QuotaPerUnit = unit * 2
	require.Equal(t, before, calculateTextToolCallSurcharge(c, info, &summary))
	require.Equal(t, audioBefore, calculateAudioQuota(audio, billingQuotaPerUnit(info)))
	require.Equal(t, unit*2, billingQuotaPerUnit(&relaycommon.RelayInfo{}))
}

func TestIntegrationRequestSettlementFailurePreservesFinalEvidence(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	session := claimTestRequest(t, req, info)
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("review_fail_wallet", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("injected database failure"))
		}
	}))
	require.Error(t, session.Settle(120))
	var pending model.IntegrationRequest
	require.NoError(t, db.First(&pending, session.integrationRequest.Id).Error)
	require.Equal(t, "settling", pending.State)
	require.Equal(t, 120, pending.ActualQuota)
	assertIntegrationBalance(t, db, 700, 0)
	require.ErrorIs(t, settleIntegrationRequest(pending.Id, 121), ErrIdempotencyConflict)
	require.NoError(t, db.Callback().Update().Remove("review_fail_wallet"))
	// Simulate process restart: retain only persisted state, not the BillingSession.
	final, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	t.Logf("after database recovery: state=%s actual=%d charged=%d", final.State, final.ActualQuota, final.ChargedQuota)
	require.Equal(t, 120, final.ActualQuota, "recovery must have final amount or durable retry evidence")
	require.Equal(t, "settled", final.State)
	assertIntegrationBalance(t, db, 880, 120)
	_, err = GetIntegrationRequest("course", req)
	require.NoError(t, err)
	assertIntegrationBalance(t, db, 880, 120)
}

func TestIntegrationRequestCancelMissingFencesLatePrepare(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	req.QuotaPerUsd = common.QuotaPerUnit
	cancelled, err := CancelIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	require.Positive(t, cancelled.SettledTime)
	replay, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, cancelled.Id, replay.Id)
	require.Equal(t, "cancelled", replay.State)
	_, err = CancelIntegrationRequest("course", req)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", req.Path, strings.NewReader(`{"model":"test"}`))
	c.Request.Header.Set(IntegrationRequestHeader, req.RequestKey)
	_, apiErr := claimIntegrationBilling(c, info, 100)
	require.NotNil(t, apiErr)
	common.CleanupBodyStorage(c)
	assertIntegrationBalance(t, db, 1000, 0)
	req.MaxQuota++
	_, err = CancelIntegrationRequest("course", req)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	_, err = PrepareIntegrationRequest("course", req)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestIntegrationRequestUnitChangeBeforePrepareCanBeCancelled(t *testing.T) {
	db, req, _ := setupIntegrationRequestTest(t)
	unit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = unit })
	common.QuotaPerUnit = unit * 2
	_, err := PrepareIntegrationRequest("course", req)
	require.ErrorIs(t, err, ErrIntegrationRequestConflict)
	var count int64
	require.NoError(t, db.Model(&model.IntegrationRequest{}).Count(&count).Error)
	require.Zero(t, count)
	cancelled, err := CancelIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, req.QuotaPerUsd, cancelled.QuotaPerUsd)
	require.Equal(t, "cancelled", cancelled.State)
	assertIntegrationBalance(t, db, 1000, 0)
}

func TestIntegrationRequestCrashAfterBillPersistence(t *testing.T) {
	for _, actual := range []int{0, 120, 450} {
		t.Run(fmt.Sprint(actual), func(t *testing.T) {
			db, req, info := setupIntegrationRequestTest(t)
			prepared, err := PrepareIntegrationRequest("course", req)
			require.NoError(t, err)
			claimTestRequest(t, req, info)
			require.NoError(t, recordIntegrationSettlement(prepared.Id, actual))
			assertIntegrationBalance(t, db, 700, 0)
			if actual > req.MaxQuota {
				var account model.IntegrationAccount
				require.NoError(t, db.First(&account, 1).Error)
				require.True(t, account.BillingSuspended)
			}
			// No BillingSession is used in recovery: only the persisted request.
			for i := 0; i < 2; i++ {
				final, err := GetIntegrationRequest("course", req)
				require.NoError(t, err)
				require.Equal(t, actual, final.ActualQuota)
				if actual > req.MaxQuota {
					require.Equal(t, "disputed", final.State)
					assertIntegrationBalance(t, db, 700, 0)
				} else {
					require.Equal(t, "settled", final.State)
					assertIntegrationBalance(t, db, 1000-actual, actual)
				}
			}
		})
	}
}

func TestIntegrationRequestRefundPreservesDisabledOrExpiredToken(t *testing.T) {
	for _, status := range []int{common.TokenStatusDisabled, common.TokenStatusExpired} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			db, req, info := setupIntegrationRequestTest(t)
			_, err := PrepareIntegrationRequest("course", req)
			require.NoError(t, err)
			session := claimTestRequest(t, req, info)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 1).Update("status", status).Error)
			require.NoError(t, session.Settle(100))
			var token model.Token
			require.NoError(t, db.First(&token, 1).Error)
			require.Equal(t, status, token.Status)
			assertIntegrationBalance(t, db, 900, 100)
		})
	}
}

func TestIntegrationRequestStaleExhaustionCannotOverwriteRefund(t *testing.T) {
	initIntegrationTokenColumns(t)
	db, req, info := setupIntegrationRequestTest(t)
	req.MaxQuota = 1000
	_, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	session := claimTestRequest(t, req, info)
	// Interleave the refund after authentication read zero and before its UPDATE.
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("refund_before_exhaustion", func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			if values, ok := tx.Statement.Dest.(map[string]interface{}); ok && len(values) == 1 && values["status"] == common.TokenStatusExhausted {
				require.NoError(t, session.Settle(100))
			}
		}
	}))
	// Avoid wrapping the auth UPDATE in a transaction so this deterministic hook
	// represents another connection's commit between SELECT and UPDATE.
	previous := model.DB
	model.DB = db.Session(&gorm.Session{SkipDefaultTransaction: true})
	_, _ = model.ValidateUserToken("test-only")
	model.DB = previous
	require.NoError(t, db.Callback().Update().Remove("refund_before_exhaustion"))
	var token model.Token
	require.NoError(t, db.First(&token, 1).Error)
	require.Equal(t, 900, token.RemainQuota)
	require.Equal(t, common.TokenStatusEnabled, token.Status)
	_, err = model.ValidateUserToken("test-only")
	require.NoError(t, err)
}
