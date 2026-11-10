package service

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func reconciliationInput(actual int) dto.IntegrationRequestReconciliationInput {
	return dto.IntegrationRequestReconciliationInput{ProviderRequestId: "provider-1", ActualQuota: &actual,
		EvidenceRef: "provider-bill:fixture-001", Reason: "已核对对应请求的最终费用凭据"}
}

func TestIntegrationRequestReconcileFailedAndSuccessfulRetry(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	failed := claimTestRequest(t, req, info)
	failed.Refund(nil)
	failed.Refund(nil)
	assertIntegrationBalance(t, db, 700, 0)
	second, nextInfo := req, *info
	second.RequestKey, nextInfo.RequestId = "retry-2", "provider-2"
	_, err = PrepareIntegrationRequest("course", second)
	require.NoError(t, err)
	retry := claimTestRequest(t, second, &nextInfo)
	require.NoError(t, retry.Settle(100))
	assertIntegrationBalance(t, db, 600, 100)
	for i := 0; i < 2; i++ {
		final, err := ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(0))
		require.NoError(t, err)
		require.Equal(t, "settled", final.State)
		require.Positive(t, final.SettledTime)
		assertIntegrationBalance(t, db, 900, 100)
	}
	var decisions []model.IntegrationRequestReconciliation
	require.NoError(t, db.Find(&decisions).Error)
	require.Len(t, decisions, 1)
	require.Equal(t, 9, decisions[0].AdminId)
	require.Equal(t, first.Id, decisions[0].RequestId)
	require.Equal(t, "provider-bill:fixture-001", decisions[0].EvidenceRef)
	detail, err := GetAdminIntegrationRequest(1, first.Id)
	require.NoError(t, err)
	require.NotNil(t, detail.Reconciliation)
	require.Equal(t, 9, detail.Reconciliation.AdminId)
	_, err = GetAdminIntegrationRequest(2, first.Id)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(1))
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	changed := reconciliationInput(0)
	changed.EvidenceRef = "different-bill:fixture-002"
	_, err = ReconcileIntegrationRequest(9, 1, first.Id, changed)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	require.ErrorIs(t, failed.Settle(1), ErrIdempotencyConflict)
	assertIntegrationBalance(t, db, 900, 100)
}

func TestIntegrationRequestReconcileRequiresExplicitEvidenceAndIdentity(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info).Refund(nil)
	for _, change := range []func(*dto.IntegrationRequestReconciliationInput){
		func(r *dto.IntegrationRequestReconciliationInput) { r.ActualQuota = nil },
		func(r *dto.IntegrationRequestReconciliationInput) { *r.ActualQuota = -1 },
		func(r *dto.IntegrationRequestReconciliationInput) { r.ProviderRequestId = "wrong" },
		func(r *dto.IntegrationRequestReconciliationInput) { r.EvidenceRef = "" },
		func(r *dto.IntegrationRequestReconciliationInput) { r.Reason = "" },
	} {
		input := reconciliationInput(0)
		change(&input)
		_, err := ReconcileIntegrationRequest(9, 1, first.Id, input)
		require.Error(t, err)
	}
	_, err = ReconcileIntegrationRequest(9, 2, first.Id, reconciliationInput(0))
	require.Error(t, err)
	assertIntegrationBalance(t, db, 700, 0)
	var count int64
	require.NoError(t, db.Model(&model.IntegrationRequestReconciliation{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestIntegrationRequestReconcileCrashRecoveryAndReadonlyListing(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info).Refund(nil)
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("fail_wallet", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("test wallet failure"))
		}
	}))
	_, err = ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(120))
	require.Error(t, err)
	rows, err := ListAdminIntegrationRequests(1, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "settling", rows[0].State)
	assertIntegrationBalance(t, db, 700, 0)
	other, err := ListAdminIntegrationRequests(2, 0)
	require.NoError(t, err)
	require.Empty(t, other)
	require.NoError(t, db.Callback().Update().Remove("fail_wallet"))
	// A new process needs only the durable bill, not the human command body.
	final, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "settled", final.State)
	assertIntegrationBalance(t, db, 880, 120)
	_, err = ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(120))
	require.NoError(t, err)
	assertIntegrationBalance(t, db, 880, 120)
}

func TestIntegrationRequestReconcileAuditFailureRollsBackDecision(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info).Refund(nil)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail_audit", func(tx *gorm.DB) {
		if tx.Statement.Table == "integration_request_reconciliations" {
			tx.AddError(errors.New("test audit failure"))
		}
	}))
	_, err = ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(0))
	require.Error(t, err)
	require.NoError(t, db.Callback().Create().Remove("fail_audit"))
	current, err := GetIntegrationRequest("course", req)
	require.NoError(t, err)
	require.Equal(t, "unresolved", current.State)
	assertIntegrationBalance(t, db, 700, 0)
}

func TestIntegrationRequestReconcileLegacyAndOverBudget(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info) // old failure rows remain running until a human verifies evidence
	final, err := ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(450))
	require.NoError(t, err)
	require.Equal(t, "disputed", final.State)
	require.Equal(t, 450, final.ActualQuota)
	assertIntegrationBalance(t, db, 700, 0)
	var account model.IntegrationAccount
	require.NoError(t, db.First(&account, 1).Error)
	require.True(t, account.BillingSuspended)
}

func TestIntegrationRequestReconcileConcurrentDecision(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	if os.Getenv("TEST_INTEGRATION_POSTGRES_DSN") == "" && os.Getenv("TEST_INTEGRATION_MYSQL_DSN") == "" {
		t.Skip("requires isolated PostgreSQL or MySQL")
	}
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(8)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info).Refund(nil)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(100))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assertIntegrationBalance(t, db, 900, 100)
	var count int64
	require.NoError(t, db.Model(&model.IntegrationRequestReconciliation{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestIntegrationRequestReconciliationMigrationPreservesExistingHold(t *testing.T) {
	db, req, info := setupIntegrationRequestTest(t)
	first, err := PrepareIntegrationRequest("course", req)
	require.NoError(t, err)
	claimTestRequest(t, req, info).Refund(nil)
	// Simulate an existing deployment before the additive audit table migration.
	require.NoError(t, db.Migrator().DropTable(&model.IntegrationRequestReconciliation{}))
	for i := 0; i < 2; i++ {
		require.NoError(t, db.AutoMigrate(&model.IntegrationRequestReconciliation{}))
		assertIntegrationBalance(t, db, 700, 0)
	}
	final, err := ReconcileIntegrationRequest(9, 1, first.Id, reconciliationInput(100))
	require.NoError(t, err)
	require.Equal(t, "settled", final.State)
	assertIntegrationBalance(t, db, 900, 100)
}
