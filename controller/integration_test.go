package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

func postIntegrationProvision(
	t *testing.T,
	integrationId string,
	req dto.IntegrationProvisionRequest,
) dto.IntegrationBalanceView {
	t.Helper()
	ctx, recorder := newAuthenticatedContext(
		t,
		http.MethodPost,
		"/api/integrations/provision",
		req,
		0,
	)
	ctx.Set(middleware.IntegrationIdContextKey, integrationId)
	IntegrationProvision(ctx)
	return decodeBotcordBalance(t, recorder)
}

func TestIntegrationProvisionNamespacesExternalUsersByService(t *testing.T) {
	setupBotcordControllerTestDB(t)

	first := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{
		ExternalUserId: "shared-user",
		DisplayName:    "Ada",
	})
	second := postIntegrationProvision(t, "partner-b", dto.IntegrationProvisionRequest{
		ExternalUserId: "shared-user",
		DisplayName:    "Ada",
	})

	if first.UserId == second.UserId {
		t.Fatalf("expected distinct users for different integrations, got %d", first.UserId)
	}
	if first.IntegrationId != "partner-a" || second.IntegrationId != "partner-b" {
		t.Fatalf("unexpected integration ids: %q and %q", first.IntegrationId, second.IntegrationId)
	}
}

func TestIntegrationProvisionIsIdempotentUnderConcurrency(t *testing.T) {
	setupBotcordControllerTestDB(t)

	type result struct {
		view dto.IntegrationBalanceView
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			view, err := service.ProvisionIntegrationAccount(
				"partner-a",
				dto.IntegrationProvisionRequest{ExternalUserId: "user-1"},
				0,
			)
			results <- result{view: view, err: err}
		}()
	}
	close(start)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("expected concurrent provisions to succeed, got %v and %v", first.err, second.err)
	}
	if first.view.UserId != second.view.UserId || first.view.Token.Id != second.view.Token.Id {
		t.Fatalf(
			"expected the same account, got users %d/%d and tokens %d/%d",
			first.view.UserId,
			second.view.UserId,
			first.view.Token.Id,
			second.view.Token.Id,
		)
	}

	var accountCount int64
	if err := model.DB.Model(&model.IntegrationAccount{}).Count(&accountCount).Error; err != nil {
		t.Fatalf("failed to count integration accounts: %v", err)
	}
	if accountCount != 1 {
		t.Fatalf("expected one integration account, got %d", accountCount)
	}
}

func TestIntegrationTopUpIsIdempotentAndRestoresExhaustedToken(t *testing.T) {
	setupBotcordControllerTestDB(t)

	provisioned := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{
		ExternalUserId: "user-1",
		InitialUsd:     1,
	})
	if err := model.DB.Model(&model.Token{}).
		Where("id = ?", provisioned.Token.Id).
		Updates(map[string]any{
			"remain_quota": 0,
			"status":       common.TokenStatusExhausted,
		}).Error; err != nil {
		t.Fatalf("failed to exhaust token: %v", err)
	}

	topUp := func() dto.IntegrationBalanceView {
		ctx, recorder := newAuthenticatedContext(
			t,
			http.MethodPost,
			"/api/integrations/topup",
			dto.IntegrationTopUpRequest{
				ExternalUserId: "user-1",
				AmountUsd:      2,
			},
			0,
		)
		ctx.Set(middleware.IntegrationIdContextKey, "partner-a")
		ctx.Request.Header.Set("Idempotency-Key", "credit-1")
		IntegrationTopUp(ctx)
		return decodeBotcordBalance(t, recorder)
	}

	first := topUp()
	second := topUp()
	expectedQuota := botcordQuotaFromUsd(3)
	if first.Quota != expectedQuota || second.Quota != expectedQuota {
		t.Fatalf("expected one top-up to produce quota %d, got %d and %d", expectedQuota, first.Quota, second.Quota)
	}
	if !second.Replayed {
		t.Fatal("expected duplicate top-up to be reported as replayed")
	}

	var token model.Token
	if err := model.DB.First(&token, provisioned.Token.Id).Error; err != nil {
		t.Fatalf("failed to load token: %v", err)
	}
	if token.Status != common.TokenStatusEnabled {
		t.Fatalf("expected exhausted token to be enabled, got status %d", token.Status)
	}
	if token.RemainQuota != botcordQuotaFromUsd(2) {
		t.Fatalf("expected token quota to be topped up once, got %d", token.RemainQuota)
	}

	var operationCount int64
	if err := model.DB.Model(&model.IntegrationOperation{}).Count(&operationCount).Error; err != nil {
		t.Fatalf("failed to count integration operations: %v", err)
	}
	if operationCount != 1 {
		t.Fatalf("expected one idempotent operation, got %d", operationCount)
	}
}

func TestIntegrationTopUpResumesSuspendedControlledBilling(t *testing.T) {
	setupBotcordControllerTestDB(t)

	postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{
		ExternalUserId:  "user-1",
		InitialUsd:      1,
		BillingProtocol: "bounded-v1",
	})
	// An overrun settlement suspended the account.
	if err := model.DB.Model(&model.IntegrationAccount{}).
		Where("external_user_id = ?", "user-1").
		Update("billing_suspended", true).Error; err != nil {
		t.Fatalf("failed to suspend account: %v", err)
	}

	ctx, recorder := newAuthenticatedContext(
		t,
		http.MethodPost,
		"/api/integrations/topup",
		dto.IntegrationTopUpRequest{ExternalUserId: "user-1", AmountUsd: 2},
		0,
	)
	ctx.Set(middleware.IntegrationIdContextKey, "partner-a")
	ctx.Request.Header.Set("Idempotency-Key", "recharge-1")
	IntegrationTopUp(ctx)
	decodeBotcordBalance(t, recorder)

	var account model.IntegrationAccount
	if err := model.DB.Where("external_user_id = ?", "user-1").First(&account).Error; err != nil {
		t.Fatalf("failed to load account: %v", err)
	}
	if account.BillingSuspended {
		t.Fatal("expected a confirmed top-up to resume controlled billing")
	}
	if account.BillingProtocol != "bounded-v1" {
		t.Fatalf("expected protocol to stay bounded-v1, got %q", account.BillingProtocol)
	}
}

func TestIntegrationTopUpRejectsIdempotencyKeyReuseWithDifferentAmount(t *testing.T) {
	setupBotcordControllerTestDB(t)
	postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{
		ExternalUserId: "user-1",
	})

	topUp := func(amount int) int {
		ctx, recorder := newAuthenticatedContext(
			t,
			http.MethodPost,
			"/api/integrations/topup",
			dto.IntegrationTopUpRequest{
				ExternalUserId: "user-1",
				AmountQuota:    amount,
			},
			0,
		)
		ctx.Set(middleware.IntegrationIdContextKey, "partner-a")
		ctx.Request.Header.Set("Idempotency-Key", "credit-1")
		IntegrationTopUp(ctx)
		return recorder.Code
	}

	if status := topUp(100); status != http.StatusOK {
		t.Fatalf("expected first top-up status 200, got %d", status)
	}
	if status := topUp(200); status != http.StatusConflict {
		t.Fatalf("expected conflicting replay status 409, got %d", status)
	}
}
