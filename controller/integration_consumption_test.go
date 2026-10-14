package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationConsumptionAuthenticationAndSafeProjection(t *testing.T) {
	setupBotcordControllerTestDB(t)
	old := model.LOG_DB
	model.LOG_DB = model.DB
	t.Cleanup(func() { model.LOG_DB = old })
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	user := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{ExternalUserId: "learner"})
	require.NoError(t, model.LOG_DB.Create(&model.Log{UserId: user.UserId, Type: model.LogTypeConsume, CreatedAt: 1700000000, ModelName: "test", Quota: 100,
		Content: "PRIVATE", Other: "PRIVATE", Ip: "PRIVATE", TokenName: "PRIVATE", RequestId: "PRIVATE"}).Error)
	t.Setenv("INTEGRATION_CLIENTS", `{"partner-a":"test-secret","partner-b":"other-secret"}`)
	router := gin.New()
	router.POST("/consumption", middleware.IntegrationAuth(), IntegrationConsumption)
	query := func(secret, integration, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/consumption", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("X-Integration-ID", integration)
		router.ServeHTTP(w, r)
		return w
	}
	body := `{"external_user_id":"learner","limit":20}`
	require.Equal(t, 401, query("wrong", "partner-a", body).Code)
	require.Equal(t, 404, query("other-secret", "partner-b", body).Code)
	require.Equal(t, 400, query("test-secret", "partner-a", `{"external_user_id":"learner","limit":51}`).Code)
	w := query("test-secret", "partner-a", body)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "PRIVATE")
	var result struct {
		Success bool                           `json:"success"`
		Data    dto.IntegrationConsumptionPage `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
	require.True(t, result.Success)
	require.Len(t, result.Data.Entries, 1)
	require.Equal(t, 100, result.Data.Entries[0].Quota)
	require.NoError(t, model.LOG_DB.Migrator().DropTable(&model.Log{}))
	w = query("test-secret", "partner-a", body)
	require.Equal(t, 503, w.Code)
	require.NotContains(t, w.Body.String(), "SELECT")
}
