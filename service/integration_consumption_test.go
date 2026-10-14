package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIntegrationConsumptionOwnershipPaginationAndReadOnly(t *testing.T) {
	db, _, _ := setupIntegrationRequestTest(t)
	old := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = old })
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	// Old token, deleted token, zero-cost calls and refund rows remain visible.
	for _, row := range []model.Log{
		{Id: 1, UserId: 1, TokenId: 99, Type: model.LogTypeConsume, Quota: 0, ModelName: "old-model"},
		{Id: 2, UserId: 1, Type: model.LogTypeConsume, Quota: 500},
		{Id: 3, UserId: 1, Type: model.LogTypeRefund, Quota: 200},
		{Id: 4, UserId: 1, Type: model.LogTypeTopup, Quota: 999},
		{Id: 5, UserId: 2, Type: model.LogTypeConsume, Quota: 999},
	} {
		require.NoError(t, db.Create(&row).Error)
	}
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("quota", 0).Error)
	require.NoError(t, db.Delete(&model.Token{}, 1).Error)
	req := dto.IntegrationConsumptionRequest{ExternalUserId: "learner", Limit: 2}
	page, err := GetIntegrationConsumption("course", req)
	require.NoError(t, err)
	require.Equal(t, 3, page.Entries[0].Id)
	require.Equal(t, model.LogTypeRefund, page.Entries[0].Type)
	require.Equal(t, 200, page.Entries[0].Quota)
	require.Equal(t, 2, *page.NextBeforeId)
	require.Equal(t, common.QuotaPerUnit, page.QuotaPerUsd)
	// New arrivals cannot shift the second page.
	require.NoError(t, db.Create(&model.Log{Id: 6, UserId: 1, Type: model.LogTypeConsume}).Error)
	req.BeforeId = *page.NextBeforeId
	next, err := GetIntegrationConsumption("course", req)
	require.NoError(t, err)
	require.Len(t, next.Entries, 1)
	require.Equal(t, 1, next.Entries[0].Id)
	require.Equal(t, 0, next.Entries[0].Quota)
	require.Nil(t, next.NextBeforeId)
	_, err = GetIntegrationConsumption("other", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
	req.ExternalUserId = "missing"
	_, err = GetIntegrationConsumption("course", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
	var count int64
	require.NoError(t, db.Model(&model.IntegrationAccount{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, 0, user.Quota)
	require.Equal(t, 0, user.UsedQuota)
}

func TestIntegrationConsumptionRejectsInvalidAndDisabled(t *testing.T) {
	db, _, _ := setupIntegrationRequestTest(t)
	for _, req := range []dto.IntegrationConsumptionRequest{
		{ExternalUserId: "learner", Limit: 0}, {ExternalUserId: "learner", Limit: 51},
		{ExternalUserId: "learner", Limit: 20, BeforeId: -1}, {Limit: 20},
	} {
		_, err := GetIntegrationConsumption("course", req)
		require.ErrorIs(t, err, ErrIntegrationConsumptionInput)
	}
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("status", common.UserStatusDisabled).Error)
	_, err := GetIntegrationConsumption("course", dto.IntegrationConsumptionRequest{ExternalUserId: "learner", Limit: 20})
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
}

func TestIntegrationConsumptionSummaryAllRetainedRecords(t *testing.T) {
	db, _, _ := setupIntegrationRequestTest(t)
	old := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = old })
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	req := dto.IntegrationConsumptionSummaryRequest{ExternalUserId: "learner"}
	empty, err := GetIntegrationConsumptionSummary("course", req)
	require.NoError(t, err)
	require.Zero(t, empty.RecordCount)
	require.Zero(t, empty.NetQuota)
	require.Nil(t, empty.FirstAt)
	require.Nil(t, empty.LastAt)
	rows := make([]model.Log, 1205)
	for i := range rows {
		rows[i] = model.Log{UserId: 1, TokenId: 99, Type: model.LogTypeConsume, Quota: 500, CreatedAt: int64(1700000000 + i)}
	}
	require.NoError(t, db.CreateInBatches(rows, 50).Error)
	require.NoError(t, db.Create(&model.Log{UserId: 1, Type: model.LogTypeRefund, Quota: 200, CreatedAt: 1700002000}).Error)
	require.NoError(t, db.Create(&model.Log{UserId: 1, Type: model.LogTypeTopup, Quota: 99999, CreatedAt: 1800000000}).Error)
	require.NoError(t, db.Create(&model.Log{UserId: 2, Type: model.LogTypeConsume, Quota: 99999}).Error)
	require.NoError(t, db.Delete(&model.Token{}, 1).Error)
	summary, err := GetIntegrationConsumptionSummary("course", req)
	require.NoError(t, err)
	require.EqualValues(t, 1206, summary.RecordCount)
	require.EqualValues(t, 602300, summary.NetQuota)
	require.EqualValues(t, 1700000000, *summary.FirstAt)
	require.EqualValues(t, 1700002000, *summary.LastAt)
	require.Equal(t, common.QuotaPerUnit, summary.QuotaPerUsd)
	_, err = GetIntegrationConsumptionSummary("other", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
	_, err = GetIntegrationConsumptionSummary("course", dto.IntegrationConsumptionSummaryRequest{})
	require.ErrorIs(t, err, ErrIntegrationConsumptionInput)
	var user model.User
	require.NoError(t, db.First(&user, 1).Error)
	require.Zero(t, user.UsedQuota)
	require.NoError(t, db.Model(&model.User{}).Where("id = 1").Update("status", common.UserStatusDisabled).Error)
	_, err = GetIntegrationConsumptionSummary("course", req)
	require.ErrorIs(t, err, ErrIntegrationAccountNotFound)
}
