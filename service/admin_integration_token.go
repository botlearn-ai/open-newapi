package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AdminIntegrationTokenView intentionally excludes API keys and external credentials.
type AdminIntegrationTokenView struct {
	AccountId      int    `json:"account_id"`
	IntegrationId  string `json:"integration_id"`
	TokenId        int    `json:"token_id"`
	Name           string `json:"name"`
	RemainQuota    int    `json:"remain_quota"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
	Status         int    `json:"status"`
	ExpiredTime    int64  `json:"expired_time"`
}

func GetAdminIntegrationTokens(userId int) ([]AdminIntegrationTokenView, error) {
	accounts := []model.IntegrationAccount{}
	if err := model.DB.Where("user_id = ?", userId).Find(&accounts).Error; err != nil {
		return nil, err
	}
	views := make([]AdminIntegrationTokenView, 0, len(accounts))
	for _, account := range accounts {
		var token model.Token
		err := model.DB.Select("id", "name", "remain_quota", "unlimited_quota", "status", "expired_time").Where("id = ? AND user_id = ?", account.TokenId, userId).First(&token).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		views = append(views, AdminIntegrationTokenView{
			AccountId: account.Id, IntegrationId: account.IntegrationId,
			TokenId: token.Id, Name: token.Name, RemainQuota: token.RemainQuota,
			UnlimitedQuota: token.UnlimitedQuota, Status: token.Status, ExpiredTime: token.ExpiredTime,
		})
	}
	return views, nil
}

// AddAdminIntegrationTokenQuota adds credit without changing user quota or used quota.
// The reserved namespace cannot collide with a normalized integration ID.
func AddAdminIntegrationTokenQuota(adminId, userId, tokenId, quota int, key string) (bool, error) {
	return addAdminIntegrationQuota(adminId, userId, tokenId, quota, key, false)
}

// AddAdminIntegrationUSD credits the user and linked token atomically.
func AddAdminIntegrationUSD(adminId, userId, tokenId int, usd float64, key string) (bool, error) {
	quota, err := IntegrationQuotaFromUsd(usd)
	if err != nil {
		return false, err
	}
	return addAdminIntegrationQuota(adminId, userId, tokenId, quota, key, true)
}

func addAdminIntegrationQuota(adminId, userId, tokenId, quota int, key string, creditUser bool) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, ErrIdempotencyKeyRequired
	}
	if adminId <= 0 || userId <= 0 || tokenId <= 0 || len(key) > maxIdempotencyKeyLength || quota <= 0 || quota > int(1e9*common.QuotaPerUnit) {
		return false, errors.New("invalid quota or idempotency key")
	}
	namespace := "@admin-token-quota"
	operation := "admin_token_topup"
	if creditUser {
		namespace = "@admin-integration-topup"
		operation = "admin_topup"
	}
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", adminId, key))))
	requestHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d", userId, tokenId, quota))))
	replayed := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var account model.IntegrationAccount
		if err := tx.Where("user_id = ? AND token_id = ?", userId, tokenId).First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrIntegrationAccountNotFound
			}
			return err
		}
		op := model.IntegrationOperation{IntegrationId: namespace, IdempotencyKeyHash: keyHash, RequestHash: requestHash, AccountId: account.Id, Operation: operation, Quota: quota, CreatedTime: common.GetTimestamp(), AdminId: adminId}
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "integration_id"}, {Name: "idempotency_key_hash"}}, DoNothing: true}).Create(&op)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var existing model.IntegrationOperation
			if err := tx.Where("integration_id = ? AND idempotency_key_hash = ?", namespace, keyHash).First(&existing).Error; err != nil {
				return err
			}
			if existing.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			replayed = true
			return nil
		}
		if creditUser {
			result = tx.Model(&model.User{}).Where("id = ?", userId).Update("quota", gorm.Expr("quota + ?", quota))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrIntegrationAccountNotFound
			}
		}
		result = tx.Model(&model.Token{}).Where("id = ? AND user_id = ? AND unlimited_quota = ? AND remain_quota <= ?", tokenId, userId, false, int(1e9*common.QuotaPerUnit)-quota).Update("remain_quota", gorm.Expr("remain_quota + ?", quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("token not found, unlimited, or quota limit exceeded")
		}
		return tx.Model(&model.Token{}).Where("id = ? AND status = ? AND remain_quota > ? AND (expired_time = ? OR expired_time > ?)", tokenId, common.TokenStatusExhausted, 0, -1, common.GetTimestamp()).Update("status", common.TokenStatusEnabled).Error
	})
	if err == nil {
		if creditUser {
			if cacheErr := model.InvalidateUserCache(userId); cacheErr != nil {
				common.SysLog(fmt.Sprintf("admin top-up user cache invalidation failed for user %d: %s", userId, cacheErr))
			}
		}
		// Also invalidate on replay, allowing a retry to repair a previous cache failure.
		if cacheErr := model.InvalidateUserTokensCache(userId); cacheErr != nil {
			common.SysLog(fmt.Sprintf("admin token top-up cache invalidation failed for user %d: %s", userId, cacheErr))
		}
	}
	return replayed, err
}
