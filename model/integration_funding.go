package model

import "github.com/QuantumNous/new-api/dto"

func GetIntegrationFunding(accountId int, integrationId string, beforeId, limit int) ([]dto.IntegrationFundingEntry, error) {
	rows := make([]IntegrationOperation, 0)
	query := DB.Model(&IntegrationOperation{}).
		Select("id, created_time, operation, quota, idempotency_key_hash").
		Where("account_id = ? AND quota > 0", accountId).
		Where("(operation = ? AND integration_id = ?) OR (operation = ? AND integration_id = ?)",
			"topup", integrationId, "admin_topup", "@admin-integration-topup")
	if beforeId > 0 {
		query = query.Where("id < ?", beforeId)
	}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	entries := make([]dto.IntegrationFundingEntry, 0, len(rows))
	for _, row := range rows {
		entry := dto.IntegrationFundingEntry{Id: row.Id, CreatedAt: row.CreatedTime, Operation: row.Operation, Quota: row.Quota}
		// Only the calling integration's own keys are needed for reconciliation.
		if row.Operation == "topup" {
			entry.IdempotencyKeyHash = row.IdempotencyKeyHash
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
