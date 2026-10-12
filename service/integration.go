package service

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxIntegrationIdLength    = 64
	maxExternalUserIdLength   = 512
	maxIdempotencyKeyLength   = 128
	maxIntegrationUsd         = 1_000_000_000
	integrationOperationTopUp = "topup"
	legacyBotcordIntegration  = "botcord"
	legacyBotcordTokenName    = "BotCord Cloud Agent"
)

var (
	ErrIntegrationAccountNotFound  = errors.New("integration account not found")
	ErrIdempotencyKeyRequired      = errors.New("idempotency key is required")
	ErrIdempotencyConflict         = errors.New("idempotency key was already used for a different request")
	ErrIntegrationIdentityConflict = errors.New("external user identity hash conflict")
)

func NormalizeIntegrationId(integrationId string) (string, error) {
	integrationId = strings.ToLower(strings.TrimSpace(integrationId))
	if integrationId == "" || len(integrationId) > maxIntegrationIdLength {
		return "", errors.New("invalid integration id")
	}
	for i, r := range integrationId {
		valid := r >= 'a' && r <= 'z' ||
			r >= '0' && r <= '9' ||
			(i > 0 && (r == '-' || r == '_' || r == '.'))
		if !valid {
			return "", errors.New("invalid integration id")
		}
	}
	return integrationId, nil
}

func IntegrationQuotaFromUsd(usd float64) (int, error) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd > maxIntegrationUsd {
		return 0, errors.New("invalid USD amount")
	}
	quota := math.Round(usd * common.QuotaPerUnit)
	if quota > float64(int(^uint(0)>>1)) {
		return 0, errors.New("quota exceeds platform limit")
	}
	return int(quota), nil
}

func integrationInitialQuota(req dto.IntegrationProvisionRequest, defaultInitialUsd float64) (int, error) {
	if req.InitialQuota < 0 || req.InitialUsd < 0 {
		return 0, errors.New("initial quota must not be negative")
	}
	if req.InitialQuota > 0 && req.InitialUsd > 0 {
		return 0, errors.New("provide only one of initial_quota or initial_usd")
	}
	if req.InitialQuota > 0 {
		if float64(req.InitialQuota) > maxIntegrationUsd*common.QuotaPerUnit {
			return 0, errors.New("initial quota exceeds platform limit")
		}
		return req.InitialQuota, nil
	}
	if req.InitialUsd > 0 {
		return IntegrationQuotaFromUsd(req.InitialUsd)
	}
	return IntegrationQuotaFromUsd(defaultInitialUsd)
}

func integrationTopUpQuota(req dto.IntegrationTopUpRequest) (int, error) {
	if req.AmountQuota < 0 || req.AmountUsd < 0 {
		return 0, errors.New("top-up amount must not be negative")
	}
	if req.AmountQuota > 0 && req.AmountUsd > 0 {
		return 0, errors.New("provide only one of amount_quota or amount_usd")
	}
	if req.AmountQuota > 0 {
		if float64(req.AmountQuota) > maxIntegrationUsd*common.QuotaPerUnit {
			return 0, errors.New("top-up quota exceeds platform limit")
		}
		return req.AmountQuota, nil
	}
	if req.AmountUsd > 0 {
		return IntegrationQuotaFromUsd(req.AmountUsd)
	}
	return 0, errors.New("amount_usd or amount_quota must be positive")
}

func integrationExternalIdentity(externalUserId string) (string, string, error) {
	externalUserId = strings.TrimSpace(externalUserId)
	if externalUserId == "" {
		return "", "", errors.New("external_user_id is required")
	}
	if len(externalUserId) > maxExternalUserIdLength {
		return "", "", errors.New("external_user_id is too long")
	}
	sum := sha256.Sum256([]byte(externalUserId))
	return externalUserId, hex.EncodeToString(sum[:]), nil
}

func integrationUsername(integrationId string, externalUserId string) string {
	sum := sha256.Sum256([]byte(integrationId + "\x00" + externalUserId))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "ia_" + strings.ToLower(encoded[:17])
}

func legacyBotcordUsername(externalUserId string) string {
	sum := sha256.Sum256([]byte(externalUserId))
	return "bc_" + hex.EncodeToString(sum[:])[:16]
}

func integrationDisplayName(integrationId string, externalUserId string, displayName string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = integrationId + " " + externalUserId
	}
	runes := []rune(name)
	if len(runes) > model.UserNameMaxLength {
		name = string(runes[:model.UserNameMaxLength])
	}
	return name
}

func integrationTokenName(integrationId string) string {
	name := "Integration " + integrationId
	runes := []rune(name)
	if len(runes) > 50 {
		return string(runes[:50])
	}
	return name
}

func createIntegrationUserAndToken(
	tx *gorm.DB,
	integrationId string,
	externalUserId string,
	displayName string,
	initialQuota int,
) (*model.User, *model.Token, error) {
	password, err := common.GenerateRandomKey(20)
	if err != nil {
		return nil, nil, err
	}
	passwordHash, err := common.Password2Hash(password)
	if err != nil {
		return nil, nil, err
	}
	user := &model.User{
		Username:    integrationUsername(integrationId, externalUserId),
		Password:    passwordHash,
		DisplayName: integrationDisplayName(integrationId, externalUserId, displayName),
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Quota:       initialQuota,
		AffCode:     common.GetRandomString(16),
	}
	if err := tx.Create(user).Error; err != nil {
		return nil, nil, err
	}

	key, err := common.GenerateKey()
	if err != nil {
		return nil, nil, err
	}
	token := &model.Token{
		UserId:             user.Id,
		Name:               integrationTokenName(integrationId),
		Key:                key,
		Status:             common.TokenStatusEnabled,
		CreatedTime:        common.GetTimestamp(),
		AccessedTime:       common.GetTimestamp(),
		ExpiredTime:        -1,
		RemainQuota:        initialQuota,
		UnlimitedQuota:     false,
		ModelLimitsEnabled: false,
	}
	if setting.DefaultUseAutoGroup {
		token.Group = "auto"
	}
	if err := tx.Create(token).Error; err != nil {
		return nil, nil, err
	}
	return user, token, nil
}

func loadLegacyBotcordAccount(
	tx *gorm.DB,
	integrationId string,
	externalUserId string,
) (*model.User, *model.Token, bool, error) {
	if integrationId != legacyBotcordIntegration {
		return nil, nil, false, nil
	}
	var user model.User
	if err := tx.Where("username = ?", legacyBotcordUsername(externalUserId)).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	var token model.Token
	if err := tx.Where(
		"user_id = ? AND name = ?",
		user.Id,
		legacyBotcordTokenName,
	).First(&token).Error; err != nil {
		return nil, nil, false, err
	}
	return &user, &token, true, nil
}

func loadIntegrationAccount(
	db *gorm.DB,
	integrationId string,
	externalUserId string,
	externalUserKey string,
) (*model.IntegrationAccount, *model.User, *model.Token, error) {
	var account model.IntegrationAccount
	if err := db.Where(
		"integration_id = ? AND external_user_key = ?",
		integrationId,
		externalUserKey,
	).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, ErrIntegrationAccountNotFound
		}
		return nil, nil, nil, err
	}
	if account.ExternalUserId != externalUserId {
		return nil, nil, nil, ErrIntegrationIdentityConflict
	}
	if account.UserId <= 0 || account.TokenId <= 0 {
		return nil, nil, nil, errors.New("integration account is incomplete")
	}

	var user model.User
	if err := db.Where("id = ?", account.UserId).First(&user).Error; err != nil {
		return nil, nil, nil, err
	}
	var token model.Token
	if err := db.Where("id = ?", account.TokenId).First(&token).Error; err != nil {
		return nil, nil, nil, err
	}
	return &account, &user, &token, nil
}

func adoptLegacyBotcordMapping(
	integrationId string,
	externalUserId string,
	externalUserKey string,
) error {
	if integrationId != legacyBotcordIntegration {
		return ErrIntegrationAccountNotFound
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		account := model.IntegrationAccount{
			IntegrationId:   integrationId,
			ExternalUserKey: externalUserKey,
			ExternalUserId:  externalUserId,
			CreatedTime:     common.GetTimestamp(),
		}
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "integration_id"},
				{Name: "external_user_key"},
			},
			DoNothing: true,
		}).Create(&account)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		user, token, found, err := loadLegacyBotcordAccount(tx, integrationId, externalUserId)
		if err != nil {
			return err
		}
		if !found {
			return ErrIntegrationAccountNotFound
		}
		return tx.Model(&account).Select("user_id", "token_id").Updates(map[string]any{
			"user_id":  user.Id,
			"token_id": token.Id,
		}).Error
	})
}

func loadIntegrationAccountWithLegacy(
	integrationId string,
	externalUserId string,
	externalUserKey string,
) (*model.IntegrationAccount, *model.User, *model.Token, error) {
	account, user, token, err := loadIntegrationAccount(
		model.DB,
		integrationId,
		externalUserId,
		externalUserKey,
	)
	if !errors.Is(err, ErrIntegrationAccountNotFound) {
		return account, user, token, err
	}
	if err := adoptLegacyBotcordMapping(
		integrationId,
		externalUserId,
		externalUserKey,
	); err != nil {
		return nil, nil, nil, err
	}
	return loadIntegrationAccount(
		model.DB,
		integrationId,
		externalUserId,
		externalUserKey,
	)
}

func buildIntegrationBalance(
	integrationId string,
	externalUserId string,
	user *model.User,
	token *model.Token,
	includeKey bool,
	replayed bool,
) dto.IntegrationBalanceView {
	view := dto.IntegrationBalanceView{
		IntegrationId:  integrationId,
		ExternalUserId: externalUserId,
		UserId:         user.Id,
		Username:       user.Username,
		Quota:          user.Quota,
		UsedQuota:      user.UsedQuota,
		QuotaPerUsd:    common.QuotaPerUnit,
		BalanceUsd:     float64(token.RemainQuota) / common.QuotaPerUnit,
		UsedUsd:        float64(token.UsedQuota) / common.QuotaPerUnit,
		Replayed:       replayed,
		Token: dto.IntegrationTokenView{
			Id:             token.Id,
			Name:           token.Name,
			RemainQuota:    token.RemainQuota,
			UsedQuota:      token.UsedQuota,
			UnlimitedQuota: token.UnlimitedQuota,
		},
	}
	if includeKey {
		view.Token.ApiKey = "sk-" + token.GetFullKey()
	}
	return view
}

func ProvisionIntegrationAccount(
	integrationId string,
	req dto.IntegrationProvisionRequest,
	defaultInitialUsd float64,
) (dto.IntegrationBalanceView, error) {
	if req.BillingProtocol != "" && req.BillingProtocol != IntegrationBillingProtocol {
		return dto.IntegrationBalanceView{}, ErrIntegrationRequestConflict
	}
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	externalUserId, externalUserKey, err := integrationExternalIdentity(req.ExternalUserId)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	initialQuota, err := integrationInitialQuota(req, defaultInitialUsd)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}

	var user model.User
	var token model.Token
	created := false
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		account := model.IntegrationAccount{
			BillingProtocol: req.BillingProtocol,
			IntegrationId:   integrationId,
			ExternalUserKey: externalUserKey,
			ExternalUserId:  externalUserId,
			CreatedTime:     common.GetTimestamp(),
		}
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "integration_id"},
				{Name: "external_user_key"},
			},
			DoNothing: true,
		}).Create(&account)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			existingAccount, existingUser, existingToken, loadErr := loadIntegrationAccount(
				tx,
				integrationId,
				externalUserId,
				externalUserKey,
			)
			if loadErr != nil {
				return loadErr
			}
			if existingAccount.BillingProtocol != req.BillingProtocol {
				return ErrIntegrationRequestConflict
			}
			account = *existingAccount
			user = *existingUser
			token = *existingToken
			return nil
		}

		legacyUser, legacyToken, adopted, adoptErr := loadLegacyBotcordAccount(
			tx,
			integrationId,
			externalUserId,
		)
		if adoptErr != nil {
			return adoptErr
		}
		if adopted {
			if req.BillingProtocol != "" {
				return ErrIntegrationRequestConflict
			}
			user = *legacyUser
			token = *legacyToken
			return tx.Model(&account).Select("user_id", "token_id").Updates(map[string]any{
				"user_id":  user.Id,
				"token_id": token.Id,
			}).Error
		}

		createdUser, createdToken, createErr := createIntegrationUserAndToken(
			tx,
			integrationId,
			externalUserId,
			req.DisplayName,
			initialQuota,
		)
		if createErr != nil {
			return createErr
		}
		user = *createdUser
		token = *createdToken
		if err := tx.Model(&account).Select("user_id", "token_id").Updates(map[string]any{
			"user_id":  user.Id,
			"token_id": token.Id,
		}).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	if created {
		if err := model.InvalidateUserTokensCache(user.Id); err != nil {
			common.SysLog(fmt.Sprintf("integration provision failed to invalidate token cache: %s", err.Error()))
		}
	}
	return buildIntegrationBalance(integrationId, externalUserId, &user, &token, created, false), nil
}

func GetIntegrationBalance(
	integrationId string,
	externalUserId string,
) (dto.IntegrationBalanceView, error) {
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	externalUserId, externalUserKey, err := integrationExternalIdentity(externalUserId)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	_, user, token, err := loadIntegrationAccountWithLegacy(
		integrationId,
		externalUserId,
		externalUserKey,
	)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	return buildIntegrationBalance(integrationId, externalUserId, user, token, false, false), nil
}

func TopUpIntegrationAccount(
	integrationId string,
	req dto.IntegrationTopUpRequest,
) (dto.IntegrationBalanceView, error) {
	integrationId, err := NormalizeIntegrationId(integrationId)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	externalUserId, externalUserKey, err := integrationExternalIdentity(req.ExternalUserId)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	quota, err := integrationTopUpQuota(req)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		return dto.IntegrationBalanceView{}, ErrIdempotencyKeyRequired
	}
	if len(idempotencyKey) > maxIdempotencyKeyLength {
		return dto.IntegrationBalanceView{}, errors.New("idempotency key is too long")
	}

	account, user, token, err := loadIntegrationAccountWithLegacy(
		integrationId,
		externalUserId,
		externalUserKey,
	)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	idempotencySum := sha256.Sum256([]byte(idempotencyKey))
	requestSum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s:%d:%d",
		integrationOperationTopUp,
		account.Id,
		quota,
	)))
	idempotencyHash := hex.EncodeToString(idempotencySum[:])
	requestHash := hex.EncodeToString(requestSum[:])
	replayed := false

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		operation := model.IntegrationOperation{
			IntegrationId:      integrationId,
			IdempotencyKeyHash: idempotencyHash,
			RequestHash:        requestHash,
			AccountId:          account.Id,
			Operation:          integrationOperationTopUp,
			Quota:              quota,
			CreatedTime:        common.GetTimestamp(),
		}
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "integration_id"},
				{Name: "idempotency_key_hash"},
			},
			DoNothing: true,
		}).Create(&operation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var existing model.IntegrationOperation
			if err := tx.Where(
				"integration_id = ? AND idempotency_key_hash = ?",
				integrationId,
				idempotencyHash,
			).First(&existing).Error; err != nil {
				return err
			}
			if existing.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			replayed = true
			return nil
		}

		userResult := tx.Model(&model.User{}).Where("id = ?", account.UserId).
			Update("quota", gorm.Expr("quota + ?", quota))
		if userResult.Error != nil {
			return userResult.Error
		}
		if userResult.RowsAffected != 1 {
			return ErrIntegrationAccountNotFound
		}
		tokenResult := tx.Model(&model.Token{}).Where("id = ?", account.TokenId).
			Update("remain_quota", gorm.Expr("remain_quota + ?", quota))
		if tokenResult.Error != nil {
			return tokenResult.Error
		}
		if tokenResult.RowsAffected != 1 {
			return ErrIntegrationAccountNotFound
		}
		if err := tx.Model(&model.Token{}).
			Where("id = ? AND status = ?", account.TokenId, common.TokenStatusExhausted).
			Update("status", common.TokenStatusEnabled).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}

	if !replayed {
		if err := model.InvalidateUserCache(user.Id); err != nil {
			common.SysLog(fmt.Sprintf("integration top-up failed to invalidate user cache: %s", err.Error()))
		}
		if err := model.InvalidateUserTokensCache(user.Id); err != nil {
			common.SysLog(fmt.Sprintf("integration top-up failed to invalidate token cache: %s", err.Error()))
		}
	}

	_, user, token, err = loadIntegrationAccountWithLegacy(
		integrationId,
		externalUserId,
		externalUserKey,
	)
	if err != nil {
		return dto.IntegrationBalanceView{}, err
	}
	return buildIntegrationBalance(integrationId, externalUserId, user, token, false, replayed), nil
}
