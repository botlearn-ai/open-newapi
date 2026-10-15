package model

import (
	"github.com/QuantumNous/new-api/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This singleton freezes the retained-history boundary once, across restarts and rollbacks.
type IntegrationConsumptionCutover struct {
	Id        int `gorm:"primaryKey;autoIncrement:false"`
	LastLogId int
}

func MigrateIntegrationConsumption(db *gorm.DB) error {
	if err := db.AutoMigrate(&IntegrationConsumptionCutover{}); err != nil {
		return err
	}
	var cutover IntegrationConsumptionCutover
	if err := db.First(&cutover, 1).Error; err == nil {
		return nil
	} else if err != gorm.ErrRecordNotFound {
		return err
	}
	var lastId int
	if err := db.Model(&Log{}).Select("COALESCE(MAX(id),0)").Scan(&lastId).Error; err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&IntegrationConsumptionCutover{Id: 1, LastLogId: lastId}).Error
}

// One aggregate over all retained rows, independent of the paginated detail API.
func GetIntegrationConsumptionSummary(userId int) (dto.IntegrationConsumptionSummary, error) {
	summary := dto.IntegrationConsumptionSummary{Groups: []dto.IntegrationConsumptionGroup{}}
	var cutover IntegrationConsumptionCutover
	if err := LOG_DB.First(&cutover, 1).Error; err != nil {
		return summary, err
	}
	scope := "CASE WHEN id <= ? THEN '' WHEN billing_scope = '' THEN 'general' ELSE billing_scope END"
	rows := LOG_DB.Model(&Log{}).Select(scope+" AS billing_scope, quota, type, created_at", cutover.LastLogId).
		Where("user_id = ? AND type IN ?", userId, []int{LogTypeConsume, LogTypeRefund})
	err := LOG_DB.Table("(?) AS scoped", rows).
		Select("billing_scope, COUNT(*) AS record_count, COALESCE(SUM(CASE WHEN type = ? THEN quota ELSE -quota END), 0) AS net_quota, MIN(created_at) AS first_at, MAX(created_at) AS last_at", LogTypeConsume).
		Group("billing_scope").Order("billing_scope").Scan(&summary.Groups).Error
	for _, group := range summary.Groups {
		summary.RecordCount += group.RecordCount
		summary.NetQuota += group.NetQuota
		if summary.FirstAt == nil || *group.FirstAt < *summary.FirstAt {
			summary.FirstAt = group.FirstAt
		}
		if summary.LastAt == nil || *group.LastAt > *summary.LastAt {
			summary.LastAt = group.LastAt
		}
	}
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
