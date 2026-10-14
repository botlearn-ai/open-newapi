package service

import (
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

var ErrIntegrationConsumptionInput = errors.New("invalid consumption query")
var ErrIntegrationConsumptionUnavailable = errors.New("consumption history unavailable")

// Read only: no account adoption, token repair, provisioning or billing writes.
func GetIntegrationConsumption(integrationId string, req dto.IntegrationConsumptionRequest) (dto.IntegrationConsumptionPage, error) {
	page := dto.IntegrationConsumptionPage{}
	if req.BeforeId < 0 || req.Limit < 1 || req.Limit > 50 {
		return page, ErrIntegrationConsumptionInput
	}
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return page, ErrIntegrationConsumptionInput
	}
	externalId, key, err := integrationExternalIdentity(req.ExternalUserId)
	if err != nil {
		return page, ErrIntegrationConsumptionInput
	}
	var account model.IntegrationAccount
	err = model.DB.Where("integration_id = ? AND external_user_key = ?", integrationId, key).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return page, ErrIntegrationAccountNotFound
	}
	if err != nil {
		return page, ErrIntegrationConsumptionUnavailable
	}
	if account.ExternalUserId != externalId || account.UserId <= 0 {
		return page, ErrIntegrationIdentityConflict
	}
	var user model.User
	if err := model.DB.Select("id, status").Where("id = ?", account.UserId).First(&user).Error; err != nil || user.Status != common.UserStatusEnabled {
		return page, ErrIntegrationAccountNotFound
	}
	unit := common.QuotaPerUnit
	if unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
		return page, ErrIntegrationConsumptionUnavailable
	}
	entries, err := model.GetIntegrationConsumption(user.Id, req.BeforeId, req.Limit)
	if err != nil {
		return page, ErrIntegrationConsumptionUnavailable
	}
	page = dto.IntegrationConsumptionPage{ExternalUserId: externalId, QuotaPerUsd: unit, Entries: entries}
	if len(entries) > req.Limit {
		page.Entries = entries[:req.Limit]
		lastId := page.Entries[len(page.Entries)-1].Id
		page.NextBeforeId = &lastId
	}
	return page, nil
}
