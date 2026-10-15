package dto

type IntegrationConsumptionSummaryRequest struct {
	ExternalUserId string `json:"external_user_id" binding:"required,max=256"`
}

type IntegrationConsumptionSummary struct {
	Groups         []IntegrationConsumptionGroup `json:"groups" gorm:"-"`
	ExternalUserId string                        `json:"external_user_id"`
	QuotaPerUsd    float64                       `json:"quota_per_usd"`
	RecordCount    int64                         `json:"record_count"`
	NetQuota       int64                         `json:"net_quota"`
	FirstAt        *int64                        `json:"first_at"`
	LastAt         *int64                        `json:"last_at"`
}

type IntegrationConsumptionGroup struct {
	BillingScope string `json:"scope"`
	RecordCount  int64  `json:"record_count"`
	NetQuota     int64  `json:"net_quota"`
	FirstAt      *int64 `json:"first_at"`
	LastAt       *int64 `json:"last_at"`
}

type IntegrationConsumptionRequest struct {
	ExternalUserId string `json:"external_user_id" binding:"required,max=256"`
	BeforeId       int    `json:"before_id" binding:"min=0"`
	Limit          int    `json:"limit" binding:"min=1,max=50"`
}

// An explicit allowlist: never serialize model.Log to integration clients.
type IntegrationConsumptionEntry struct {
	Id               int    `json:"id"`
	CreatedAt        int64  `json:"created_at"`
	Type             int    `json:"type"`
	ModelName        string `json:"model_name"`
	Quota            int    `json:"quota"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

type IntegrationConsumptionPage struct {
	ExternalUserId string                        `json:"external_user_id"`
	QuotaPerUsd    float64                       `json:"quota_per_usd"`
	Entries        []IntegrationConsumptionEntry `json:"entries"`
	NextBeforeId   *int                          `json:"next_before_id"`
}
