package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func scopeCredential(t *testing.T, scope integrationScope, secret string) string {
	t.Helper()
	body, err := common.Marshal(scope)
	require.NoError(t, err)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("integration-scope-v1:" + encoded))
	return "isc1." + encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestIntegrationScopeSignatureExpiryAndOwner(t *testing.T) {
	t.Setenv("INTEGRATION_CLIENTS", `{"course":"secret-a","other":"secret-b"}`)
	scope := integrationScope{Integration: "course", User: "learner", Course: "a134df19-ad21-4fe9-935a-6fcc8b3336b1", Key: "sk-test", Expires: time.Now().Unix() + 3300}
	actual, err := readIntegrationScope(scopeCredential(t, scope, "secret-a"))
	require.NoError(t, err)
	require.Equal(t, scope, actual)
	for _, change := range []func(*integrationScope){
		func(s *integrationScope) { s.Expires = time.Now().Unix() - 1 },
		func(s *integrationScope) { s.Expires = time.Now().Unix() + 7200 },
		func(s *integrationScope) { s.Course = "not-a-course" },
		func(s *integrationScope) { s.Integration = "other" },
	} {
		invalid := scope
		change(&invalid)
		_, err = readIntegrationScope(scopeCredential(t, invalid, "secret-a"))
		require.Error(t, err)
	}
	_, err = readIntegrationScope(scopeCredential(t, scope, "wrong"))
	require.Error(t, err)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old; sql, _ := db.DB(); sql.Close() })
	require.NoError(t, db.AutoMigrate(&model.IntegrationAccount{}))
	digest := sha256.Sum256([]byte("learner"))
	require.NoError(t, db.Create(&model.IntegrationAccount{IntegrationId: "course", ExternalUserId: "learner", ExternalUserKey: hex.EncodeToString(digest[:]), UserId: 7}).Error)
	require.NoError(t, validateIntegrationScopeOwner(scope, &model.Token{UserId: 7}))
	require.Error(t, validateIntegrationScopeOwner(scope, &model.Token{UserId: 8}))
	scope.User = "other"
	require.Error(t, validateIntegrationScopeOwner(scope, &model.Token{UserId: 7}))
}

func TestIntegrationScopeTokenAuthPreservesOriginalLimits(t *testing.T) {
	redisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = redisEnabled })
	t.Setenv("INTEGRATION_CLIENTS", `{"course":"secret-a"}`)
	old := model.DB
	oldMaster, oldPath := common.IsMasterNode, common.SQLitePath
	oldSQLite, oldMySQL, oldPG := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	t.Setenv("SQL_DSN", "local")
	common.IsMasterNode, common.SQLitePath = false, ":memory:"
	common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = false, false, false
	require.NoError(t, model.InitDB())
	db := model.DB
	t.Cleanup(func() {
		sql, _ := db.DB()
		sql.Close()
		model.DB = old
		common.IsMasterNode, common.SQLitePath = oldMaster, oldPath
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = oldSQLite, oldMySQL, oldPG
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.IntegrationAccount{}))
	require.NoError(t, db.Create(&model.User{Id: 17, Username: "scope-test", Status: common.UserStatusEnabled, Group: "default", Quota: 100}).Error)
	require.NoError(t, db.Create(&model.Token{Id: 19, UserId: 17, Key: "scopetestkey", Status: common.TokenStatusEnabled, RemainQuota: 7, ExpiredTime: -1, ModelLimitsEnabled: true, ModelLimits: "allowed-model"}).Error)
	digest := sha256.Sum256([]byte("learner"))
	require.NoError(t, db.Create(&model.IntegrationAccount{IntegrationId: "course", ExternalUserId: "learner", ExternalUserKey: hex.EncodeToString(digest[:]), UserId: 17}).Error)
	scope := integrationScope{Integration: "course", User: "learner", Course: "a134df19-ad21-4fe9-935a-6fcc8b3336b1", Key: "sk-scopetestkey", Expires: time.Now().Unix() + 3300}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(TokenAuth())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		require.Equal(t, scope.Course, c.GetString("billing_scope"))
		require.Equal(t, 7, c.GetInt("token_quota"))
		require.True(t, c.GetBool("token_model_limit_enabled"))
		require.Equal(t, 19, c.GetInt("token_id"))
		c.Status(204)
	})
	call := func() int {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+scopeCredential(t, scope, "secret-a"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, 204, call())
	require.NoError(t, db.Model(&model.Token{}).Where("id = 19").Update("status", common.TokenStatusDisabled).Error)
	require.Equal(t, 401, call())
}
