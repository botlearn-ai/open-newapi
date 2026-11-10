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

func TestReconciliationMigrationUpgradesDeployedManifest(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		baseline(t, db)
		v1, err := manifest(db)
		require.NoError(t, err)
		require.NoError(t, apply(db, v1, fmt.Sprintf("%x", sha256.Sum256([]byte(v1)))))
		require.NoError(t, applySpending(db))
		require.False(t, db.Migrator().HasTable(reconciliationTable))

		// Exercise the exact fixed-manifest command path, not AutoMigrate.
		require.NoError(t, Run(context.Background(), db))
		live, frozen := &gorm.Statement{DB: db}, &gorm.Statement{DB: db}
		require.NoError(t, live.Parse(&model.IntegrationRequestReconciliation{}))
		require.NoError(t, frozen.Parse(&requestReconciliationV3{}))
		require.Equal(t, live.Schema.DBNames, frozen.Schema.DBNames)
		for _, name := range live.Schema.DBNames {
			require.Equal(t, db.Migrator().FullDataTypeOf(live.Schema.FieldsByDBName[name]).SQL,
				db.Migrator().FullDataTypeOf(frozen.Schema.FieldsByDBName[name]).SQL, name)
		}
		row := model.IntegrationRequestReconciliation{
			RequestId: 41, AdminId: 1, ProviderRequestId: "test-provider", ActualQuota: 0,
			EvidenceRef: "test-evidence", Reason: "已核验零费用", CreatedTime: 123,
		}
		require.NoError(t, db.Create(&row).Error)
		require.Positive(t, row.Id)
		require.NoError(t, Run(context.Background(), db))
		var saved model.IntegrationRequestReconciliation
		require.NoError(t, db.First(&saved, row.Id).Error)
		require.Equal(t, row, saved)
		row.Id = 0
		require.Error(t, db.Create(&row).Error, "one audit decision per request")
		var quota int
		require.NoError(t, db.Table("users").Select("quota").Scan(&quota).Error)
		require.Equal(t, 17489582, quota, "migration must not change balances")
	})
}

func TestReconciliationMigrationResumesAndRejectsDrift(t *testing.T) {
	for _, scenario := range []string{"partial", "checksum", "nonunique", "column", "missing_table"} {
		t.Run(scenario, func(t *testing.T) {
			databases(t, func(t *testing.T, db *gorm.DB) {
				baseline(t, db)
				require.NoError(t, Run(context.Background(), db))
				switch scenario {
				case "partial":
					require.NoError(t, db.Exec("DELETE FROM "+ledgerTable+" WHERE version = ?", reconciliationVersion).Error)
					require.NoError(t, db.Migrator().DropIndex(reconciliationTable, reconciliationIndex))
					require.NoError(t, Run(context.Background(), db))
					require.NoError(t, Run(context.Background(), db))
				case "checksum":
					require.NoError(t, db.Exec("UPDATE "+ledgerTable+" SET checksum = 'changed' WHERE version = ?", reconciliationVersion).Error)
					require.ErrorContains(t, Run(context.Background(), db), "history does not match")
				case "nonunique":
					require.NoError(t, db.Migrator().DropIndex(reconciliationTable, reconciliationIndex))
					require.NoError(t, db.Exec("CREATE INDEX "+reconciliationIndex+" ON "+reconciliationTable+"(request_id)").Error)
					require.ErrorContains(t, Run(context.Background(), db), "index shape mismatch")
				case "column":
					require.NoError(t, db.Migrator().DropColumn(&requestReconciliationV3{}, "reason"))
					require.ErrorContains(t, Run(context.Background(), db), "required columns missing")
				case "missing_table":
					require.NoError(t, db.Migrator().DropTable(reconciliationTable))
					require.Error(t, Run(context.Background(), db))
				}
			})
		})
	}
}
