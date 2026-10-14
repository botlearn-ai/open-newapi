package model

import "github.com/QuantumNous/new-api/dto"

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
