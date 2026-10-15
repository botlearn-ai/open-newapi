package service

import (
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

var ErrIntegrationFundingInput = errors.New("invalid funding query")
var ErrIntegrationFundingUnavailable = errors.New("funding history unavailable")

// Read-only projection: never provisions accounts, repairs tokens or credits balances.
func GetIntegrationFunding(integrationId string, req dto.IntegrationFundingRequest) (dto.IntegrationFundingPage, error) {
	page := dto.IntegrationFundingPage{}
	if req.BeforeId < 0 || req.Limit < 1 || req.Limit > 50 {
		return page, ErrIntegrationFundingInput
	}
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return page, ErrIntegrationFundingInput
	}
	externalId, key, err := integrationExternalIdentity(req.ExternalUserId)
	if err != nil {
		return page, ErrIntegrationFundingInput
	}
	var account model.IntegrationAccount
	err = model.DB.Where("integration_id = ? AND external_user_key = ?", integrationId, key).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return page, ErrIntegrationAccountNotFound
	}
	if err != nil {
		return page, ErrIntegrationFundingUnavailable
	}
	if account.ExternalUserId != externalId || account.UserId <= 0 {
		return page, ErrIntegrationIdentityConflict
	}
	var user model.User
	err = model.DB.Select("id, status").Where("id = ?", account.UserId).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && user.Status != common.UserStatusEnabled) {
		return page, ErrIntegrationAccountNotFound
	}
	unit := common.QuotaPerUnit
	if err != nil || unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
		return page, ErrIntegrationFundingUnavailable
	}
	entries, err := model.GetIntegrationFunding(account.Id, integrationId, req.BeforeId, req.Limit)
	if err != nil {
		return page, ErrIntegrationFundingUnavailable
	}
	page = dto.IntegrationFundingPage{ExternalUserId: externalId, QuotaPerUsd: unit, Entries: entries}
	if len(entries) > req.Limit {
		page.Entries = entries[:req.Limit]
		lastId := page.Entries[len(page.Entries)-1].Id
		page.NextBeforeId = &lastId
	}
	return page, nil
}
