package model

import "github.com/QuantumNous/new-api/dto"

// One aggregate over all retained rows, independent of the paginated detail API.
func GetIntegrationConsumptionSummary(userId int) (dto.IntegrationConsumptionSummary, error) {
	var summary dto.IntegrationConsumptionSummary
	err := LOG_DB.Model(&Log{}).
		Select("COUNT(*) AS record_count, COALESCE(SUM(CASE WHEN type = ? THEN quota ELSE -quota END), 0) AS net_quota, MIN(created_at) AS first_at, MAX(created_at) AS last_at", LogTypeConsume).
		Where("user_id = ? AND type IN ?", userId, []int{LogTypeConsume, LogTypeRefund}).
		Scan(&summary).Error
	return summary, err
}

func GetIntegrationConsumption(userId, beforeId, limit int) ([]dto.IntegrationConsumptionEntry, error) {
	entries := make([]dto.IntegrationConsumptionEntry, 0)
	query := LOG_DB.Model(&Log{}).
		Select("id, created_at, type, model_name, quota, prompt_tokens, completion_tokens").
		Where("user_id = ? AND type IN ?", userId, []int{LogTypeConsume, LogTypeRefund})
	if beforeId > 0 {
		query = query.Where("id < ?", beforeId)
	}
	err := query.Order("id DESC").Limit(limit + 1).Find(&entries).Error
	return entries, err
}
