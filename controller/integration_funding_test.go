package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIntegrationFundingAuthenticationAndSafeProjection(t *testing.T) {
	setupBotcordControllerTestDB(t)
	user := postIntegrationProvision(t, "partner-a", dto.IntegrationProvisionRequest{ExternalUserId: "learner"})
	var account model.IntegrationAccount
	require.NoError(t, model.DB.Where("user_id = ?", user.UserId).First(&account).Error)
	require.NoError(t, model.DB.Create(&model.IntegrationOperation{AccountId: account.Id, IntegrationId: "@admin-integration-topup", Operation: "admin_topup", Quota: 100, RequestHash: "PRIVATE", IdempotencyKeyHash: "PRIVATE", AdminId: 99}).Error)
	t.Setenv("INTEGRATION_CLIENTS", `{"partner-a":"test-secret","partner-b":"other-secret"}`)
	router := gin.New()
	router.POST("/funding", middleware.IntegrationAuth(), IntegrationFunding)
	query := func(secret, integration, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/funding", strings.NewReader(body))
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
	for _, field := range []string{"PRIVATE", "admin_id", "account_id", "token_id", "\"user_id\""} {
		require.NotContains(t, w.Body.String(), field)
	}
	var result struct {
		Data dto.IntegrationFundingPage `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result.Data.Entries, 1)
	require.Equal(t, 100, result.Data.Entries[0].Quota)
	require.NoError(t, model.DB.Migrator().DropTable(&model.IntegrationOperation{}))
	w = query("test-secret", "partner-a", body)
	require.Equal(t, 503, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "SELECT")
}
