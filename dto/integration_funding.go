package dto

type IntegrationFundingRequest struct {
	ExternalUserId string `json:"external_user_id" binding:"required,max=256"`
	BeforeId       int    `json:"before_id" binding:"min=0"`
	Limit          int    `json:"limit" binding:"min=1,max=50"`
}

// Funding operations, not token allowance repairs or text-only administrator logs.
type IntegrationFundingEntry struct {
	Id                 int    `json:"id"`
	CreatedAt          int64  `json:"created_at"`
	Operation          string `json:"operation"`
	Quota              int    `json:"quota"`
	IdempotencyKeyHash string `json:"idempotency_key_hash,omitempty"`
}

type IntegrationFundingPage struct {
	ExternalUserId string                    `json:"external_user_id"`
	QuotaPerUsd    float64                   `json:"quota_per_usd"`
	Entries        []IntegrationFundingEntry `json:"entries"`
	NextBeforeId   *int                      `json:"next_before_id"`
}
