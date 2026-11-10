package service

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListAdminIntegrationRequests is strictly read-only, including settling rows.
func ListAdminIntegrationRequests(userId, before int) ([]model.IntegrationRequest, error) {
	rows := []model.IntegrationRequest{}
	accounts := model.DB.Model(&model.IntegrationAccount{}).Select("id").Where("user_id = ?", userId)
	query := model.DB.Where("account_id IN (?)", accounts)
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	err := query.Order("id DESC").Limit(50).Find(&rows).Error
	return rows, err
}

type AdminIntegrationRequestDetail struct {
	Request        model.IntegrationRequest                `json:"request"`
	Reconciliation *model.IntegrationRequestReconciliation `json:"reconciliation"`
}

func GetAdminIntegrationRequest(userId, requestId int) (AdminIntegrationRequestDetail, error) {
	var detail AdminIntegrationRequestDetail
	accounts := model.DB.Model(&model.IntegrationAccount{}).Select("id").Where("user_id = ?", userId)
	if err := model.DB.Where("id = ? AND account_id IN (?)", requestId, accounts).First(&detail.Request).Error; err != nil {
		return detail, err
	}
	var decision model.IntegrationRequestReconciliation
	err := model.DB.Where("request_id = ?", requestId).First(&decision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return detail, nil
	}
	if err == nil {
		detail.Reconciliation = &decision
	}
	return detail, err
}

// ReconcileIntegrationRequest saves evidence and the immutable amount atomically.
// A wallet failure leaves settling, recoverable by query without another decision.
// Only the root-session controller exposes this command; integration keys cannot call it.
func ReconcileIntegrationRequest(adminId, userId, requestId int, input dto.IntegrationRequestReconciliationInput) (model.IntegrationRequest, error) {
	var result model.IntegrationRequest
	input.EvidenceRef, input.Reason = strings.TrimSpace(input.EvidenceRef), strings.TrimSpace(input.Reason)
	if adminId <= 0 || userId <= 0 || requestId <= 0 || input.ActualQuota == nil || *input.ActualQuota < 0 ||
		*input.ActualQuota > 1000000000000 || input.ProviderRequestId == "" || len(input.ProviderRequestId) > 64 ||
		len(input.EvidenceRef) < 8 || len(input.EvidenceRef) > 1000 || len(input.Reason) < 8 || len(input.Reason) > 2000 {
		return result, ErrIntegrationRequestConflict
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&result, requestId).Error; err != nil {
			return err
		}
		var account model.IntegrationAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND billing_protocol = ?", result.AccountId, userId, IntegrationBillingProtocol).
			First(&account).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result, requestId).Error; err != nil {
			return err
		}
		if result.ProviderRequestId != input.ProviderRequestId {
			return ErrIntegrationRequestConflict
		}
		var existing model.IntegrationRequestReconciliation
		// Use a current read after waiting for the account lock. MySQL's default
		// repeatable-read snapshot may predate another transaction's decision.
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&existing).Error
		if err == nil {
			if existing.AdminId != adminId || existing.ActualQuota != *input.ActualQuota ||
				existing.ProviderRequestId != input.ProviderRequestId || existing.EvidenceRef != input.EvidenceRef || existing.Reason != input.Reason {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if result.State != "running" && result.State != "unresolved" {
			return ErrIntegrationRequestConflict
		}
		decision := model.IntegrationRequestReconciliation{
			RequestId: requestId, AdminId: adminId, ProviderRequestId: input.ProviderRequestId,
			ActualQuota: *input.ActualQuota, EvidenceRef: input.EvidenceRef, Reason: input.Reason,
			CreatedTime: common.GetTimestamp(),
		}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		return recordIntegrationSettlementTx(tx, requestId, *input.ActualQuota)
	})
	if err == nil {
		err = settleIntegrationRequest(requestId, *input.ActualQuota)
	}
	if err == nil {
		err = model.DB.First(&result, requestId).Error
	}
	return result, err
}
