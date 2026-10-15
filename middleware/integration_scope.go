package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/google/uuid"
)

// A signed attribution wrapper keeps the original token's limits and revocation.
// It authorizes reporting context only, never a balance or course entitlement.
type integrationScope struct {
	Integration string `json:"integration"`
	User        string `json:"user"`
	Course      string `json:"course"`
	Key         string `json:"key"`
	Expires     int64  `json:"expires"`
}

func readIntegrationScope(value string) (integrationScope, error) {
	var scope integrationScope
	invalid := errors.New("invalid integration scope")
	parts := strings.Split(value, ".")
	if len(value) > 4096 || len(parts) != 3 || parts[0] != "isc1" {
		return scope, invalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return scope, invalid
	}
	if err = common.Unmarshal(body, &scope); err != nil {
		return scope, invalid
	}
	normalized, err := service.NormalizeIntegrationId(scope.Integration)
	if err != nil || normalized != scope.Integration {
		return scope, invalid
	}
	secret, err := configuredIntegrationSecret(scope.Integration)
	if err != nil {
		return scope, invalid
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("integration-scope-v1:" + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return scope, invalid
	}
	now := time.Now().Unix()
	if scope.Expires <= now || scope.Expires > now+3600 || scope.User == "" || len(scope.User) > 256 || scope.Key == "" || strings.Contains(scope.Key, ".") {
		return scope, invalid
	}
	if scope.Course != "general" {
		id, err := uuid.Parse(scope.Course)
		if err != nil || id.String() != scope.Course {
			return scope, invalid
		}
	}
	return scope, nil
}

func validateIntegrationScopeOwner(scope integrationScope, token *model.Token) error {
	digest := sha256.Sum256([]byte(scope.User))
	var account model.IntegrationAccount
	// Match both the exact external identity and its indexed key.
	err := model.DB.Where("integration_id = ? AND external_user_key = ?", scope.Integration, hex.EncodeToString(digest[:])).First(&account).Error
	if err != nil || account.ExternalUserId != scope.User || account.UserId != token.UserId {
		return errors.New("invalid integration scope owner")
	}
	return nil
}
