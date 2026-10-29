package service

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

var ErrIntegrationCredentialConflict = errors.New("integration credential recovery rejected")

// RecoverIntegrationCredential only reads an existing, explicitly matched account.
// It never provisions, rotates keys, repairs status, or changes credit.
func RecoverIntegrationCredential(integrationId string, req dto.IntegrationCredentialRecoveryRequest) (dto.IntegrationBalanceView, error) {
	empty := dto.IntegrationBalanceView{}
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return empty, ErrIntegrationCredentialConflict
	}
	externalId, key, err := integrationExternalIdentity(req.ExternalUserId)
	if err != nil || req.ExpectedUserId <= 0 || req.ExpectedTokenId <= 0 {
		return empty, ErrIntegrationCredentialConflict
	}
	account, user, token, err := loadIntegrationAccount(model.DB, integrationId, externalId, key)
	if err != nil {
		return empty, err
	}
	if user.Id != req.ExpectedUserId || token.Id != req.ExpectedTokenId || token.UserId != user.Id ||
		user.Status != common.UserStatusEnabled || token.Status != common.TokenStatusEnabled ||
		account.BillingProtocol != IntegrationBillingProtocol || user.UsedQuota != 0 || token.UsedQuota != 0 ||
		account.BillingSuspended || (token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp()) || token.GetFullKey() == "" {
		return empty, ErrIntegrationCredentialConflict
	}
	var requests int64
	if err := model.DB.Model(&model.IntegrationRequest{}).Where("account_id = ?", account.Id).Count(&requests).Error; err != nil {
		return empty, err
	}
	if requests != 0 {
		return empty, ErrIntegrationCredentialConflict
	}
	return buildIntegrationBalance(integrationId, externalId, user, token, true, false), nil
}
