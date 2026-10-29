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

func TestIntegrationCredentialRecoveryPreservesAccountAndCredit(t *testing.T) {
	setupBotcordControllerTestDB(t)
	if err := model.DB.AutoMigrate(&model.IntegrationRequest{}); err != nil {
		t.Fatal(err)
	}
	initial := postIntegrationProvision(t, "course", dto.IntegrationProvisionRequest{ExternalUserId: "recover-user", InitialUsd: 5, BillingProtocol: service.IntegrationBillingProtocol})
	req := dto.IntegrationCredentialRecoveryRequest{ExternalUserId: "recover-user", ExpectedUserId: initial.UserId, ExpectedTokenId: initial.Token.Id}
	for i := 0; i < 2; i++ {
		ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/integrations/credentials/recover", req, 0)
		ctx.Set(middleware.IntegrationIdContextKey, "course")
		IntegrationRecoverCredential(ctx)
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential response must not be cached")
		}
		got := decodeBotcordBalance(t, recorder)
		if got.Token.ApiKey == "" || got.Token.ApiKey != initial.Token.ApiKey || got.UserId != initial.UserId || got.Token.Id != initial.Token.Id || got.Quota != initial.Quota || got.Token.RemainQuota != initial.Token.RemainQuota {
			t.Fatal("recovery changed credential, account, or credit")
		}
	}
	ordinary := postIntegrationProvision(t, "course", dto.IntegrationProvisionRequest{ExternalUserId: "recover-user", InitialUsd: 5, BillingProtocol: service.IntegrationBillingProtocol})
	if ordinary.Token.ApiKey != "" || ordinary.Quota != initial.Quota {
		t.Fatal("ordinary replay leaked key or duplicated credit")
	}
}

func TestIntegrationCredentialRecoveryRejectsWrongTargetAndDisabledState(t *testing.T) {
	setupBotcordControllerTestDB(t)
	if err := model.DB.AutoMigrate(&model.IntegrationRequest{}); err != nil {
		t.Fatal(err)
	}
	initial := postIntegrationProvision(t, "course", dto.IntegrationProvisionRequest{ExternalUserId: "recover-user", InitialUsd: 5, BillingProtocol: service.IntegrationBillingProtocol})
	valid := dto.IntegrationCredentialRecoveryRequest{ExternalUserId: "recover-user", ExpectedUserId: initial.UserId, ExpectedTokenId: initial.Token.Id}
	cases := []struct {
		integration string
		req         dto.IntegrationCredentialRecoveryRequest
	}{
		{"other", valid},
		{"course", dto.IntegrationCredentialRecoveryRequest{ExternalUserId: "missing", ExpectedUserId: initial.UserId, ExpectedTokenId: initial.Token.Id}},
		{"course", dto.IntegrationCredentialRecoveryRequest{ExternalUserId: "recover-user", ExpectedUserId: initial.UserId + 1, ExpectedTokenId: initial.Token.Id}},
		{"course", dto.IntegrationCredentialRecoveryRequest{ExternalUserId: "recover-user", ExpectedUserId: initial.UserId, ExpectedTokenId: initial.Token.Id + 1}},
	}
	for _, tc := range cases {
		got, err := service.RecoverIntegrationCredential(tc.integration, tc.req)
		if err == nil || got.Token.ApiKey != "" {
			t.Fatal("mismatched target exposed a credential")
		}
	}
	if err := model.DB.Model(&model.Token{}).Where("id = ?", initial.Token.Id).Update("status", common.TokenStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	got, err := service.RecoverIntegrationCredential("course", valid)
	if err == nil || got.Token.ApiKey != "" {
		t.Fatal("disabled token recovered")
	}
	var count int64
	model.DB.Model(&model.IntegrationAccount{}).Count(&count)
	if count != 1 {
		t.Fatal("recovery created an account")
	}
}
