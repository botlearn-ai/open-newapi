package controller

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRequestReconciliationControllerGuards(t *testing.T) {
	setupBotcordControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.IntegrationRequest{}, &model.IntegrationRequestReconciliation{}))
	account := postIntegrationProvision(t, "course", dto.IntegrationProvisionRequest{
		ExternalUserId: "learner", BillingProtocol: service.IntegrationBillingProtocol, InitialUsd: 1,
	})
	row, err := service.PrepareIntegrationRequest("course", dto.IntegrationRequestInput{
		ExternalUserId: "learner", RequestKey: "failed-request", Path: "/v1/responses",
		BodySHA256: strings.Repeat("a", 64), MaxQuota: 100, QuotaPerUsd: common.QuotaPerUnit,
	})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&row).Updates(map[string]any{"state": "unresolved", "provider_request_id": "provider-1"}).Error)
	call := func(role int, accessToken bool, userId int, body map[string]any) (int, tokenAPIResponse) {
		ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/", body, 999)
		ctx.Set("role", role)
		ctx.Set("use_access_token", accessToken)
		ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(userId)}, {Key: "request_id", Value: fmt.Sprint(row.Id)}}
		AdminReconcileIntegrationRequest(ctx)
		return recorder.Code, decodeAPIResponse(t, recorder)
	}
	body := map[string]any{"provider_request_id": "provider-1", "actual_quota": 0,
		"evidence_ref": "provider-bill:fixture-001", "reason": "已核对最终费用证据"}
	for _, role := range []int{0, common.RoleCommonUser, common.RoleAdminUser} {
		status, _ := call(role, false, account.UserId, body)
		require.Equal(t, http.StatusForbidden, status)
	}
	status, _ := call(common.RoleRootUser, true, account.UserId, body)
	require.Equal(t, http.StatusForbidden, status)
	_, response := call(common.RoleRootUser, false, account.UserId, map[string]any{"provider_request_id": "provider-1"})
	require.False(t, response.Success)
	_, response = call(common.RoleRootUser, false, account.UserId+999, body)
	require.False(t, response.Success)
	_, response = call(common.RoleRootUser, false, account.UserId, body)
	require.True(t, response.Success)
	var result model.IntegrationRequest
	require.NoError(t, common.Unmarshal(response.Data, &result))
	require.Equal(t, "settled", result.State)
	require.Zero(t, result.ActualQuota)
	_, response = call(common.RoleRootUser, false, account.UserId, body)
	require.True(t, response.Success)
	var count int64
	require.NoError(t, model.DB.Model(&model.IntegrationRequestReconciliation{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	// The listing neither returns token material nor executes settling recovery.
	require.NoError(t, model.DB.Model(&row).Update("state", "settling").Error)
	ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/", nil, 999)
	ctx.Set("role", common.RoleRootUser)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(account.UserId)}}
	AdminListIntegrationRequests(ctx)
	listed := decodeAPIResponse(t, recorder)
	require.True(t, listed.Success)
	require.Contains(t, string(listed.Data), `"state":"settling"`)
	require.NotContains(t, string(listed.Data), "api_key")
	require.NotContains(t, string(listed.Data), "body_sha256")
}
