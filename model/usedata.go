package model

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// QuotaData 柱状图数据
type QuotaData struct {
	Id        int    `json:"id"`
	UserID    int    `json:"user_id" gorm:"index;uniqueIndex:idx_quota_data_bucket,priority:1"`
	Username  string `json:"username" gorm:"index:idx_qdt_model_user_name,priority:2;size:64;default:'';uniqueIndex:idx_quota_data_bucket,priority:2"`
	ModelName string `json:"model_name" gorm:"index:idx_qdt_model_user_name,priority:1;size:64;default:'';uniqueIndex:idx_quota_data_bucket,priority:3"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index:idx_qdt_created_at,priority:2;uniqueIndex:idx_quota_data_bucket,priority:4"`
	TokenUsed int    `json:"token_used" gorm:"default:0"`
	Count     int    `json:"count" gorm:"default:0"`
	Quota     int    `json:"quota" gorm:"default:0"`
}

func UpdateQuotaData() {
	for {
		if common.DataExportEnabled {
			common.SysLog("正在更新数据看板数据...")
			SaveQuotaDataCache()
		}
		time.Sleep(time.Duration(common.DataExportInterval) * time.Minute)
	}
}

type quotaDataBucket struct {
	userID    int
	username  string
	modelName string
	createdAt int64
}

var CacheQuotaData = make(map[quotaDataBucket]*QuotaData)
var CacheQuotaDataLock = sync.Mutex{}

func logQuotaDataCache(userId int, username string, modelName string, quota int, createdAt int64, tokenUsed int) {
	key := quotaDataBucket{userID: userId, username: username, modelName: modelName, createdAt: createdAt}
	quotaData, ok := CacheQuotaData[key]
	if ok {
		quotaData.Count += 1
		quotaData.Quota += quota
		quotaData.TokenUsed += tokenUsed
	} else {
		quotaData = &QuotaData{
			UserID:    userId,
			Username:  username,
			ModelName: modelName,
			CreatedAt: createdAt,
			Count:     1,
			Quota:     quota,
			TokenUsed: tokenUsed,
		}
	}
	CacheQuotaData[key] = quotaData
}

func LogQuotaData(userId int, username string, modelName string, quota int, createdAt int64, tokenUsed int) {
	// 只精确到小时
	createdAt = createdAt - (createdAt % 3600)

	CacheQuotaDataLock.Lock()
	defer CacheQuotaDataLock.Unlock()
	logQuotaDataCache(userId, username, modelName, quota, createdAt, tokenUsed)
}

func SaveQuotaDataCache() {
	CacheQuotaDataLock.Lock()
	defer CacheQuotaDataLock.Unlock()
	saved := 0
	for _, quotaData := range CacheQuotaData {
		if err := quotaData.AddToBucket(DB); err != nil {
			// An interrupted write may have committed. Reconcile the chart from logs
			// before reapplying a delta whose outcome is unknown.
			continue
		}
		saved++
	}
	failed := len(CacheQuotaData) - saved
	CacheQuotaData = make(map[quotaDataBucket]*QuotaData)
	if failed > 0 {
		common.SysError(fmt.Sprintf("数据看板有%d条增量写入结果待核对", failed))
	}
	common.SysLog(fmt.Sprintf("数据看板共保存%d条增量", saved))
}

// AddToBucket atomically adds the usage delta to its hourly dashboard bucket.
func (quotaData QuotaData) AddToBucket(db *gorm.DB) error {
	quotaData.Id = 0
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "username"}, {Name: "model_name"}, {Name: "created_at"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"count":      gorm.Expr("quota_data.count + ?", quotaData.Count),
			"quota":      gorm.Expr("quota_data.quota + ?", quotaData.Quota),
			"token_used": gorm.Expr("quota_data.token_used + ?", quotaData.TokenUsed),
		}),
	}).Create(&quotaData).Error
}

func GetQuotaDataByUsername(username string, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	// 从quota_data表中查询数据
	err = DB.Table("quota_data").Where("username = ? and created_at >= ? and created_at <= ?", username, startTime, endTime).Find(&quotaDatas).Error
	return quotaDatas, err
}

func GetQuotaDataByUserId(userId int, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	// 从quota_data表中查询数据
	err = DB.Table("quota_data").Where("user_id = ? and created_at >= ? and created_at <= ?", userId, startTime, endTime).Find(&quotaDatas).Error
	return quotaDatas, err
}

func GetQuotaDataGroupByUser(startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	err = DB.Table("quota_data").
		Select("username, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("username, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func GetAllQuotaDates(startTime int64, endTime int64, username string) (quotaData []*QuotaData, err error) {
	if username != "" {
		return GetQuotaDataByUsername(username, startTime, endTime)
	}
	var quotaDatas []*QuotaData
	// 从quota_data表中查询数据
	// only select model_name, sum(count) as count, sum(quota) as quota, model_name, created_at from quota_data group by model_name, created_at;
	//err = DB.Table("quota_data").Where("created_at >= ? and created_at <= ?", startTime, endTime).Find(&quotaDatas).Error
	err = DB.Table("quota_data").Select("model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, created_at").Where("created_at >= ? and created_at <= ?", startTime, endTime).Group("model_name, created_at").Find(&quotaDatas).Error
	return quotaDatas, err
}
