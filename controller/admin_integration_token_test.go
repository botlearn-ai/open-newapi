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
	"gorm.io/gorm"
)

func TestAdminIntegrationTokenQuota(t *testing.T) {
	setupBotcordControllerTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Log{}))
	account := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{ExternalUserId: "user-1", InitialUsd: 5})
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", account.Token.Id).Updates(map[string]any{"remain_quota": 0, "used_quota": 123, "status": common.TokenStatusExhausted}).Error)
	call := func(role, userId, tokenId, quota int, key string) tokenAPIResponse {
		ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/", map[string]any{"quota": quota}, 999)
		ctx.Set("role", role)
		ctx.Set("username", "admin")
		ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(userId)}, {Key: "token_id", Value: fmt.Sprint(tokenId)}}
		ctx.Request.Header.Set("Idempotency-Key", key)
		AdminAddIntegrationTokenQuota(ctx)
		return decodeAPIResponse(t, recorder)
	}
	require.False(t, call(common.RoleCommonUser, account.UserId, account.Token.Id, 500, "a").Success)
	require.False(t, call(common.RoleAdminUser, account.UserId, account.Token.Id, 0, "a").Success)
	require.False(t, call(common.RoleAdminUser, account.UserId, account.Token.Id, 500, "").Success)
	require.True(t, call(common.RoleAdminUser, account.UserId, account.Token.Id, 500, "a").Success)
	// Simulate an intervening consumption; retry must not credit again or overwrite it.
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", account.Token.Id).Update("remain_quota", gorm.Expr("remain_quota - ?", 100)).Error)
	require.True(t, call(common.RoleAdminUser, account.UserId, account.Token.Id, 500, "a").Success)
	require.False(t, call(common.RoleAdminUser, account.UserId, account.Token.Id, 600, "a").Success)
	var token model.Token
	require.NoError(t, model.DB.First(&token, account.Token.Id).Error)
	require.Equal(t, 400, token.RemainQuota)
	require.Equal(t, 123, token.UsedQuota)
	require.Equal(t, common.TokenStatusEnabled, token.Status)
	user, err := model.GetUserById(account.UserId, false)
	require.NoError(t, err)
	require.Equal(t, int(5*common.QuotaPerUnit), user.Quota)
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Find(&logs).Error)
	require.Len(t, logs, 1)
	require.Contains(t, logs[0].Other, `"admin_id":999`)
	var operations []model.IntegrationOperation
	require.NoError(t, model.DB.Find(&operations).Error)
	require.Len(t, operations, 1)
	require.Equal(t, 999, operations[0].AdminId)
	// Listing does not disclose key material.
	ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/", nil, 999)
	ctx.Set("role", common.RoleAdminUser)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(account.UserId)}}
	AdminGetIntegrationTokens(ctx)
	require.True(t, decodeAPIResponse(t, recorder).Success)
	require.NotContains(t, recorder.Body.String(), token.Key)
	require.NotContains(t, recorder.Body.String(), `"key"`)
	// Both viewing and crediting respect the existing administrator hierarchy.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", account.UserId).Update("role", common.RoleAdminUser).Error)
	require.False(t, call(common.RoleAdminUser, account.UserId, account.Token.Id, 500, "b").Success)
	require.True(t, call(common.RoleRootUser, account.UserId, account.Token.Id, 500, "b").Success)
}

func TestAdminIntegrationTokenQuotaGuards(t *testing.T) {
	for _, scenario := range []string{"wrong-owner", "unlimited", "deleted", "overflow", "disabled", "expired", "still-negative"} {
		t.Run(scenario, func(t *testing.T) {
			setupBotcordControllerTestDB(t)
			a := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{ExternalUserId: "user-1"})
			userId := a.UserId
			updates := map[string]any{}
			wantError := true
			switch scenario {
			case "wrong-owner":
				userId++
			case "unlimited":
				updates["unlimited_quota"] = true
			case "deleted":
				require.NoError(t, model.DB.Delete(&model.Token{}, a.Token.Id).Error)
			case "overflow":
				updates["remain_quota"] = int(1e9 * common.QuotaPerUnit)
			case "disabled":
				updates["status"] = common.TokenStatusDisabled
				wantError = false
			case "expired":
				updates["status"] = common.TokenStatusExhausted
				updates["expired_time"] = common.GetTimestamp() - 1
				wantError = false
			case "still-negative":
				updates["status"] = common.TokenStatusExhausted
				updates["remain_quota"] = -1000
				wantError = false
			}
			if len(updates) > 0 {
				require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", a.Token.Id).Updates(updates).Error)
			}
			_, err := service.AddAdminIntegrationTokenQuota(999, userId, a.Token.Id, 500, "a")
			if wantError {
				require.Error(t, err)
				var count int64
				require.NoError(t, model.DB.Model(&model.IntegrationOperation{}).Count(&count).Error)
				require.Zero(t, count, "failed credit must roll back idempotency record")
			} else {
				require.NoError(t, err)
				var token model.Token
				require.NoError(t, model.DB.First(&token, a.Token.Id).Error)
				require.Equal(t, updates["status"], token.Status)
			}
		})
	}
}

func TestAdminIntegrationTokenQuotaConcurrentReplay(t *testing.T) {
	setupBotcordControllerTestDB(t)
	a := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{ExternalUserId: "user-1"})
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			_, err := service.AddAdminIntegrationTokenQuota(999, a.UserId, a.Token.Id, 500, "same")
			results <- err
		}()
	}
	close(start)
	for range 8 {
		require.NoError(t, <-results)
	}
	var token model.Token
	require.NoError(t, model.DB.First(&token, a.Token.Id).Error)
	require.Equal(t, 500, token.RemainQuota)
	_, err = service.AddAdminIntegrationTokenQuota(999, a.UserId, a.Token.Id, -1, "negative")
	require.Error(t, err)
	_, err = service.AddAdminIntegrationTokenQuota(999, a.UserId, a.Token.Id, 1, strings.Repeat("x", 300))
	require.Error(t, err)
}
