package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const IntegrationRequestHeader = "X-Integration-Request-Key"
const IntegrationBillingProtocol = "bounded-v1"

var ErrIntegrationRequestConflict = errors.New("integration request state or payload conflict")
var ErrIntegrationRequestBudget = errors.New("integration request budget insufficient")

func validIntegrationQuotaUnit(unit float64) bool {
	// Every provider quota unit must be exactly representable in micro-USD.
	return !math.IsNaN(unit) && !math.IsInf(unit, 0) && unit >= 1 && unit <= 1000000 &&
		unit == math.Trunc(unit) && 1000000%int(unit) == 0
}

func supportedIntegrationPath(path string) bool {
	return path == "/v1/chat/completions" || path == "/v1/responses" || path == "/v1/messages"
}

func validIntegrationAuthorization(req dto.IntegrationRequestInput) bool {
	digest, err := hex.DecodeString(req.BodySHA256)
	return err == nil && len(digest) == sha256.Size && req.BodySHA256 == strings.ToLower(req.BodySHA256) &&
		req.MaxQuota > 0 && req.MaxQuota <= 1000000000000 && len(req.RequestKey) > 0 &&
		len(req.RequestKey) <= 128 && supportedIntegrationPath(req.Path)
}

func requestAccount(db *gorm.DB, integrationId, externalId string) (*model.IntegrationAccount, error) {
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return nil, err
	}
	externalId, key, err := integrationExternalIdentity(externalId)
	if err != nil {
		return nil, err
	}
	account, _, _, err := loadIntegrationAccount(db, integrationId, externalId, key)
	return account, err
}

// PrepareIntegrationRequest stores immutable authorization. No model call is made.
// Only accounts explicitly provisioned with bounded billing can use this protocol.
func PrepareIntegrationRequest(integrationId string, req dto.IntegrationRequestInput) (model.IntegrationRequest, error) {
	var result model.IntegrationRequest
	if !validIntegrationQuotaUnit(req.QuotaPerUsd) || req.QuotaPerUsd != common.QuotaPerUnit || !validIntegrationAuthorization(req) {
		return result, ErrIntegrationRequestConflict
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		account, err := requestAccount(tx, integrationId, req.ExternalUserId)
		if err != nil {
			return err
		}
		// Serialize creation against cancellation tombstones and model claims.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(account, account.Id).Error; err != nil {
			return err
		}
		if account.BillingProtocol != IntegrationBillingProtocol || account.BillingSuspended {
			return ErrIntegrationRequestConflict
		}
		result = model.IntegrationRequest{
			AccountId: account.Id, RequestKey: req.RequestKey, Path: req.Path,
			BodySHA256: req.BodySHA256, MaxQuota: req.MaxQuota, QuotaPerUsd: req.QuotaPerUsd,
			State: "ready", CreatedTime: common.GetTimestamp(),
		}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&result)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 0 {
			if err := tx.Where("account_id = ? AND request_key = ?", account.Id, req.RequestKey).First(&result).Error; err != nil {
				return err
			}
			if result.Path != req.Path || result.BodySHA256 != req.BodySHA256 || result.MaxQuota != req.MaxQuota || result.QuotaPerUsd != req.QuotaPerUsd {
				return ErrIdempotencyConflict
			}
		}
		return nil
	})
	return result, err
}

func GetIntegrationRequest(integrationId string, req dto.IntegrationRequestInput) (model.IntegrationRequest, error) {
	var result model.IntegrationRequest
	account, err := requestAccount(model.DB, integrationId, req.ExternalUserId)
	if err != nil {
		return result, err
	}
	err = model.DB.Where("account_id = ? AND request_key = ?", account.Id, req.RequestKey).First(&result).Error
	if err == nil && result.State == "settling" {
		// Query is a recovery command: a persisted bill can finish after a process
		// restart without replaying inference or trusting an amount from the caller.
		if err = settleIntegrationRequest(result.Id, result.ActualQuota); err != nil {
			return result, err
		}
		err = model.DB.First(&result, result.Id).Error
	}
	return result, err
}

// Cancel only an unclaimed authorization. Running requests must be reconciled.
func CancelIntegrationRequest(integrationId string, req dto.IntegrationRequestInput) (model.IntegrationRequest, error) {
	var result model.IntegrationRequest
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		account, err := requestAccount(tx, integrationId, req.ExternalUserId)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(account, account.Id).Error; err != nil {
			return err
		}
		if account.BillingProtocol != IntegrationBillingProtocol {
			return ErrIntegrationRequestConflict
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("account_id = ? AND request_key = ?", account.Id, req.RequestKey).First(&result).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if !validIntegrationAuthorization(req) || !validIntegrationQuotaUnit(req.QuotaPerUsd) {
				return ErrIntegrationRequestConflict
			}
			// A durable tombstone proves this key cannot execute even if prepare
			// arrives after the cancellation response. It does not reserve funds.
			result = model.IntegrationRequest{AccountId: account.Id, RequestKey: req.RequestKey,
				Path: req.Path, BodySHA256: req.BodySHA256, MaxQuota: req.MaxQuota,
				QuotaPerUsd: req.QuotaPerUsd, State: "cancelled",
				CreatedTime: common.GetTimestamp(), SettledTime: common.GetTimestamp()}
			return tx.Create(&result).Error
		}
		if err != nil {
			return err
		}
		if req.Path != "" && (result.Path != req.Path || result.BodySHA256 != req.BodySHA256 ||
			result.MaxQuota != req.MaxQuota || result.QuotaPerUsd != req.QuotaPerUsd) {
			return ErrIdempotencyConflict
		}
		if result.State == "cancelled" {
			return nil
		}
		if result.State != "ready" {
			return ErrIntegrationRequestConflict
		}
		updated := tx.Model(&result).Where("state = ?", "ready").Updates(map[string]interface{}{"state": "cancelled", "settled_time": common.GetTimestamp()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrIntegrationRequestConflict
		}
		result.State = "cancelled"
		return nil
	})
	return result, err
}

// ValidateIntegrationBillingRoute prevents controlled users bypassing the request
// ledger through other tokens, unbudgeted requests, free models or task APIs.
func ValidateIntegrationBillingRoute(c *gin.Context, userId int) error {
	var accounts []model.IntegrationAccount
	if err := model.DB.Where("user_id = ?", userId).Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		if account.BillingProtocol == IntegrationBillingProtocol {
			if account.BillingSuspended || c.Request.Method != http.MethodPost || !supportedIntegrationPath(c.Request.URL.Path) ||
				c.GetHeader(IntegrationRequestHeader) == "" || c.GetInt("token_id") != account.TokenId {
				return ErrIntegrationRequestConflict
			}
			return nil
		}
	}
	if c.GetHeader(IntegrationRequestHeader) != "" {
		return ErrIntegrationRequestConflict
	}
	return nil
}

func claimIntegrationBilling(c *gin.Context, info *relaycommon.RelayInfo, estimated int) (*BillingSession, *types.NewAPIError) {
	body, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, body); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	_, _ = body.Seek(0, io.SeekStart)
	digest := hex.EncodeToString(hash.Sum(nil))
	var request model.IntegrationRequest
	var account model.IntegrationAccount
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND token_id = ? AND billing_protocol = ?", info.UserId, info.TokenId, IntegrationBillingProtocol).First(&account).Error; err != nil {
			return err
		}
		if account.BillingSuspended {
			return ErrIntegrationRequestConflict
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("account_id = ? AND request_key = ?", account.Id, c.GetHeader(IntegrationRequestHeader)).First(&request).Error; err != nil {
			return err
		}
		if request.State != "ready" || request.Path != c.Request.URL.Path || request.BodySHA256 != digest {
			return ErrIntegrationRequestConflict
		}
		if request.MaxQuota < estimated || request.QuotaPerUsd != common.QuotaPerUnit {
			return ErrIntegrationRequestBudget
		}
		if info.TieredBillingSnapshot != nil && info.TieredBillingSnapshot.QuotaPerUnit != request.QuotaPerUsd {
			return ErrIntegrationRequestBudget
		}
		// Conditional writes protect balances on SQLite as well as row-locking databases.
		claimed := tx.Model(&request).Where("state = ?", "ready").Updates(map[string]interface{}{"state": "running", "provider_request_id": info.RequestId})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return ErrIntegrationRequestConflict
		}
		user := tx.Model(&model.User{}).Where("id = ? AND status = ? AND quota >= ?", info.UserId, common.UserStatusEnabled, request.MaxQuota).Update("quota", gorm.Expr("quota - ?", request.MaxQuota))
		if user.Error != nil {
			return user.Error
		}
		if user.RowsAffected != 1 {
			return ErrIntegrationRequestBudget
		}
		token := tx.Model(&model.Token{}).Where("id = ? AND user_id = ? AND status = ? AND (expired_time = -1 OR expired_time > ?) AND (unlimited_quota = ? OR remain_quota >= ?)", info.TokenId, info.UserId, common.TokenStatusEnabled, common.GetTimestamp(), true, request.MaxQuota).Update("remain_quota", gorm.Expr("remain_quota - ?", request.MaxQuota))
		if token.Error != nil {
			return token.Error
		}
		if token.RowsAffected != 1 {
			return ErrIntegrationRequestBudget
		}
		request.State = "running"
		return nil
	})
	if err != nil {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusConflict, types.ErrOptionWithSkipRetry())
	}
	invalidateIntegrationBillingCache(info.UserId)
	info.FinalPreConsumedQuota, info.BillingSource = request.MaxQuota, BillingSourceWallet
	return &BillingSession{relayInfo: info, integrationRequest: &request, preConsumedQuota: request.MaxQuota}, nil
}

func invalidateIntegrationBillingCache(userId int) {
	if err := model.InvalidateUserCache(userId); err != nil {
		common.SysLog("integration billing user cache invalidation failed")
	}
	if err := model.InvalidateUserTokensCache(userId); err != nil {
		common.SysLog("integration billing token cache invalidation failed")
	}
}

// Persist the final amount before attempting wallet changes. A failed wallet
// transaction leaves a settling record that a later query can safely complete.
func recordIntegrationSettlement(requestId int, actual int) error {
	if actual < 0 {
		return ErrIntegrationRequestConflict
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var request model.IntegrationRequest
		if err := tx.First(&request, requestId).Error; err != nil {
			return err
		}
		var account model.IntegrationAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, request.AccountId).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&request, requestId).Error; err != nil {
			return err
		}
		if request.State == "settling" || request.State == "settled" || request.State == "disputed" {
			if request.ActualQuota != actual {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if request.State != "running" {
			return ErrIntegrationRequestConflict
		}
		if actual > request.MaxQuota {
			if err := tx.Model(&account).Update("billing_suspended", true).Error; err != nil {
				return err
			}
		}
		updated := tx.Model(&request).Where("state = ?", "running").Updates(map[string]interface{}{"state": "settling", "actual_quota": actual})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrIntegrationRequestConflict
		}
		return nil
	})
}

// The wallet, token, counters and terminal state still commit atomically.
func settleIntegrationRequest(requestId int, actual int) error {
	if err := recordIntegrationSettlement(requestId, actual); err != nil {
		return err
	}
	var userId int
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var request model.IntegrationRequest
		if err := tx.First(&request, requestId).Error; err != nil {
			return err
		}
		// Match claim's account -> request lock order.
		var account model.IntegrationAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, request.AccountId).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&request, requestId).Error; err != nil {
			return err
		}
		if request.State == "settled" || request.State == "disputed" {
			if request.ActualQuota != actual {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if request.State != "settling" || request.ActualQuota != actual {
			return ErrIntegrationRequestConflict
		}
		userId = account.UserId
		if actual > request.MaxQuota {
			// Keep the full computed cost and original hold; no unauthorized extra debit.
			if err := tx.Model(&account).Update("billing_suspended", true).Error; err != nil {
				return err
			}
			return tx.Model(&request).Updates(map[string]interface{}{"state": "disputed", "actual_quota": actual, "settled_time": common.GetTimestamp()}).Error
		}
		refund := request.MaxQuota - actual
		updated := tx.Model(&request).Where("state = ?", "settling").Updates(map[string]interface{}{"state": "settled", "charged_quota": actual, "settled_time": common.GetTimestamp()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrIntegrationRequestConflict
		}
		user := tx.Model(&model.User{}).Where("id = ?", account.UserId).Updates(map[string]interface{}{
			"quota":         gorm.Expr("quota + ?", refund),
			"used_quota":    gorm.Expr("used_quota + ?", actual),
			"request_count": gorm.Expr("request_count + 1"),
		})
		if user.Error != nil {
			return user.Error
		}
		if user.RowsAffected != 1 {
			return ErrIntegrationRequestConflict
		}
		token := tx.Model(&model.Token{}).Where("id = ?", account.TokenId).Updates(map[string]interface{}{"remain_quota": gorm.Expr("remain_quota + ?", refund), "used_quota": gorm.Expr("used_quota + ?", actual)})
		if token.Error != nil {
			return token.Error
		}
		if token.RowsAffected != 1 {
			return ErrIntegrationRequestConflict
		}
		// Preserve administrative disables and expiration; only a funded token
		// exhausted by the temporary hold may become enabled again.
		if err := tx.Model(&model.Token{}).Where("id = ? AND status = ? AND remain_quota > 0 AND (expired_time = -1 OR expired_time > ?)", account.TokenId, common.TokenStatusExhausted, common.GetTimestamp()).Update("status", common.TokenStatusEnabled).Error; err != nil {
			return err
		}
		return nil
	})
	if err == nil && userId != 0 {
		invalidateIntegrationBillingCache(userId)
	}
	return err
}

func integrationBudgetError() error {
	return fmt.Errorf("%w: supplementary reservation denied", ErrIntegrationRequestBudget)
}

func integrationBilling(info *relaycommon.RelayInfo) bool {
	session, ok := info.Billing.(*BillingSession)
	return ok && session.integrationRequest != nil
}

func billingQuotaPerUnit(info *relaycommon.RelayInfo) float64 {
	if info != nil {
		if session, ok := info.Billing.(*BillingSession); ok && session.integrationRequest != nil {
			return session.integrationRequest.QuotaPerUsd
		}
	}
	return common.QuotaPerUnit
}
