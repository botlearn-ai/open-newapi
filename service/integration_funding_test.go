package service

import (
	"fmt"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestIntegrationFundingOwnershipPaginationAndReadOnly(t *testing.T) {
	db, _, _ := setupIntegrationRequestTest(t)
	require.NoError(t, db.AutoMigrate(&model.IntegrationOperation{}))
	rows := []model.IntegrationOperation{
		{Id: 1, AccountId: 1, IntegrationId: "course", Operation: "topup", Quota: 500},
		{Id: 2, AccountId: 1, IntegrationId: "@admin-integration-topup", Operation: "admin_topup", Quota: 1000, AdminId: 999},
		{Id: 3, AccountId: 1, IntegrationId: "@admin-token-quota", Operation: "admin_token_topup", Quota: 999},
		{Id: 4, AccountId: 2, IntegrationId: "course", Operation: "topup", Quota: 999},
		{Id: 5, AccountId: 1, IntegrationId: "other", Operation: "topup", Quota: 999},
		{Id: 6, AccountId: 1, IntegrationId: "course", Operation: "topup", Quota: 0},
		{Id: 7, AccountId: 2, IntegrationId: "@admin-integration-topup", Operation: "admin_topup", Quota: 999},
	}
	for i := range rows {
		rows[i].IdempotencyKeyHash = fmt.Sprintf("%064d", rows[i].Id)
		rows[i].CreatedTime = int64(1700000000 + i)
		require.NoError(t, db.Create(&rows[i]).Error)
	}
	// Deleted tokens and exhausted balances must not trigger repairs during reads.
	require.NoError(t, db.Delete(&model.Token{}, 1).Error)
	req := dto.IntegrationFundingRequest{ExternalUserId: "learner", Limit: 1}
	page, err := GetIntegrationFunding("course", req)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, 2, page.Entries[0].Id)
	require.Empty(t, page.Entries[0].IdempotencyKeyHash)
	require.Equal(t, 2, *page.NextBeforeId)
	require.Equal(t, common.QuotaPerUnit, page.QuotaPerUsd)
	require.NoError(t, db.Create(&model.IntegrationOperation{Id: 8, AccountId: 1, IntegrationId: "course", Operation: "topup", Quota: 2000, IdempotencyKeyHash: "new"}).Error)
	req.BeforeId = *page.NextBeforeId
	page, err = GetIntegrationFunding("course", req)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, 1, page.Entries[0].Id)
	require.Equal(t, rows[0].IdempotencyKeyHash, page.Entries[0].IdempotencyKeyHash)
	require.Nil(t, page.NextBeforeId)
	_, err = GetIntegrationFunding("other", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
	req.ExternalUserId = "missing"
	_, err = GetIntegrationFunding("course", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, 1000, user.Quota)
	require.Zero(t, user.UsedQuota)
	var count int64
	require.NoError(t, db.Model(&model.IntegrationAccount{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&model.Token{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestIntegrationFundingInvalidUnavailableAndDisabled(t *testing.T) {
	db, _, _ := setupIntegrationRequestTest(t)
	for _, req := range []dto.IntegrationFundingRequest{
		{ExternalUserId: "learner", Limit: 0}, {ExternalUserId: "learner", Limit: 51},
		{ExternalUserId: "learner", Limit: 20, BeforeId: -1}, {Limit: 20},
	} {
		_, err := GetIntegrationFunding("course", req)
		require.ErrorIs(t, err, ErrIntegrationFundingInput)
	}
	req := dto.IntegrationFundingRequest{ExternalUserId: "learner", Limit: 20}
	_, err := GetIntegrationFunding("course", req) // missing operation table
	require.ErrorIs(t, err, ErrIntegrationFundingUnavailable)
	require.NoError(t, db.AutoMigrate(&model.IntegrationOperation{}))
	page, err := GetIntegrationFunding("course", req)
	require.NoError(t, err)
	require.Empty(t, page.Entries)
	require.NotNil(t, page.Entries)
	oldUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = oldUnit })
	common.QuotaPerUnit = math.NaN()
	_, err = GetIntegrationFunding("course", req)
	require.ErrorIs(t, err, ErrIntegrationFundingUnavailable)
	common.QuotaPerUnit = oldUnit
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("status", common.UserStatusDisabled).Error)
	_, err = GetIntegrationFunding("course", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
}
