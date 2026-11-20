package migration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyQuotaData struct {
	ID        int
	UserID    int
	Username  string `gorm:"size:64;default:''"`
	ModelName string `gorm:"size:64;default:''"`
	CreatedAt int64  `gorm:"type:bigint"`
	TokenUsed int    `gorm:"default:0"`
	Count     int    `gorm:"default:0"`
	Quota     int    `gorm:"default:0"`
}

func (legacyQuotaData) TableName() string { return quotaDataTable }

func TestQuotaDataMigrationUpgradesDeployedManifest(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		baseline(t, db)
		v1, err := manifest(db)
		require.NoError(t, err)
		require.NoError(t, apply(db, v1, fmt.Sprintf("%x", sha256.Sum256([]byte(v1)))))
		require.NoError(t, applySpending(db))
		require.NoError(t, applyReconciliation(db))
		row := legacyQuotaData{UserID: 2, Username: "learner", ModelName: "example", CreatedAt: 7200, Count: 5, Quota: 17, TokenUsed: 30}
		require.NoError(t, db.Create(&row).Error)
		require.False(t, db.Migrator().HasIndex(quotaDataTable, quotaDataIndex))

		require.NoError(t, Run(context.Background(), db))
		require.NoError(t, Run(context.Background(), db))
		var saved legacyQuotaData
		require.NoError(t, db.First(&saved).Error)
		require.Equal(t, row, saved)
		row.ID = 0
		require.Error(t, db.Create(&row).Error)
	})
}

func TestQuotaDataMigrationRejectsAmbiguousHistory(t *testing.T) {
	for _, scenario := range []string{"duplicates", "null_key", "null_counter", "index_drift", "incomplete_index"} {
		t.Run(scenario, func(t *testing.T) {
			databases(t, func(t *testing.T, db *gorm.DB) {
				baseline(t, db)
				switch scenario {
				case "duplicates":
					rows := []legacyQuotaData{
						{UserID: 2, Username: "learner", ModelName: "example", CreatedAt: 7200, Count: 3, Quota: 5, TokenUsed: 7},
						{UserID: 2, Username: "learner", ModelName: "example", CreatedAt: 7200, Count: 8, Quota: 9, TokenUsed: 10},
					}
					require.NoError(t, db.Create(&rows).Error)
					require.ErrorIs(t, Run(context.Background(), db), ErrQuotaDataNeedsReview)
					var actual []legacyQuotaData
					require.NoError(t, db.Order("id").Find(&actual).Error)
					require.Equal(t, rows, actual)
					require.False(t, db.Migrator().HasIndex(quotaDataTable, quotaDataIndex))
				case "null_key":
					require.NoError(t, db.Exec("INSERT INTO quota_data (created_at) VALUES (7200)").Error)
					require.ErrorIs(t, Run(context.Background(), db), ErrQuotaDataNeedsReview)
				case "null_counter":
					require.NoError(t, db.Exec("INSERT INTO quota_data (user_id, created_at, count) VALUES (2, 7200, NULL)").Error)
					require.ErrorIs(t, Run(context.Background(), db), ErrQuotaDataNeedsReview)
				case "index_drift":
					require.NoError(t, db.Exec("CREATE INDEX "+quotaDataIndex+" ON quota_data ("+quotaDataColumns+")").Error)
					require.ErrorContains(t, Run(context.Background(), db), "index shape mismatch")
				case "incomplete_index":
					ddl := quotaDataCreateIndex + " WHERE user_id > 0"
					if db.Dialector.Name() == "mysql" {
						ddl = "CREATE UNIQUE INDEX " + quotaDataIndex + " ON quota_data (user_id, username(16), model_name, created_at)"
					}
					require.NoError(t, db.Exec(ddl).Error)
					require.ErrorContains(t, Run(context.Background(), db), "index shape mismatch")
				}
				if db.Migrator().HasTable(ledgerTable) {
					var versions int64
					require.NoError(t, db.Table(ledgerTable).Where("version = ?", quotaDataVersion).Count(&versions).Error)
					require.Zero(t, versions)
				}
			})
		})
	}
}

func TestQuotaDataConcurrentWritersAccumulateOnce(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		baseline(t, db)
		require.NoError(t, Run(context.Background(), db))
		sqlDB, err := db.DB()
		require.NoError(t, err)
		if db.Dialector.Name() == "sqlite" {
			sqlDB.SetMaxOpenConns(1)
		}

		const writers = 24
		ready := make(chan struct{})
		errs := make(chan error, writers)
		for i := 1; i <= writers; i++ {
			go func(value int) {
				<-ready
				delta := model.QuotaData{UserID: 2, Username: "learner", ModelName: "example", CreatedAt: 7200, Count: 1, Quota: value, TokenUsed: 2 * value}
				errs <- delta.AddToBucket(db)
			}(i)
		}
		close(ready)
		for i := 0; i < writers; i++ {
			require.NoError(t, <-errs)
		}
		var rows []model.QuotaData
		require.NoError(t, db.Find(&rows).Error)
		require.Len(t, rows, 1)
		sum := writers * (writers + 1) / 2
		require.Equal(t, writers, rows[0].Count)
		require.Equal(t, sum, rows[0].Quota)
		require.Equal(t, 2*sum, rows[0].TokenUsed)
	})
}
