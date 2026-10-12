package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRequestControllerContract(t *testing.T) {
	setupBotcordControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.IntegrationRequest{}))
	postIntegrationProvision(t, "course", dto.IntegrationProvisionRequest{
		ExternalUserId: "learner", BillingProtocol: service.IntegrationBillingProtocol,
	})
	req := dto.IntegrationRequestInput{ExternalUserId: "learner", RequestKey: "r1", Path: "/v1/responses", BodySHA256: strings.Repeat("a", 64), MaxQuota: 100, QuotaPerUsd: common.QuotaPerUnit}
	call := func(handler gin.HandlerFunc, partner string, input dto.IntegrationRequestInput) (int, model.IntegrationRequest) {
		ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/integrations/requests", input, 0)
		ctx.Set(middleware.IntegrationIdContextKey, partner)
		handler(ctx)
		var result model.IntegrationRequest
		if recorder.Code == http.StatusOK {
			response := decodeAPIResponse(t, recorder)
			require.True(t, response.Success)
			require.NoError(t, common.Unmarshal(response.Data, &result))
		}
		return recorder.Code, result
	}
	status, prepared := call(IntegrationPrepareRequest, "course", req)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ready", prepared.State)
	status, _ = call(IntegrationGetRequest, "another-partner", req)
	require.Equal(t, http.StatusNotFound, status)
	status, cancelled := call(IntegrationCancelRequest, "course", req)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "cancelled", cancelled.State)
	require.Positive(t, cancelled.SettledTime)
	req.RequestKey = "missing"
	status, _ = call(IntegrationGetRequest, "course", req)
	require.Equal(t, http.StatusNotFound, status)
}

func TestIntegrationRequestCannotConvertLegacyAccount(t *testing.T) {
	setupBotcordControllerTestDB(t)
	postIntegrationProvision(t, "course", dto.IntegrationProvisionRequest{ExternalUserId: "learner"})
	_, err := service.ProvisionIntegrationAccount("course", dto.IntegrationProvisionRequest{
		ExternalUserId: "learner", BillingProtocol: service.IntegrationBillingProtocol,
	}, 0)
	require.ErrorIs(t, err, service.ErrIntegrationRequestConflict)
	var account model.IntegrationAccount
	require.NoError(t, model.DB.First(&account).Error)
	require.Empty(t, account.BillingProtocol)
}
