package model

import (
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type preScopeLog struct {
	Id        int `gorm:"primaryKey"`
	UserId    int
	CreatedAt int64
	Type      int
	Quota     int
}

func (preScopeLog) TableName() string { return "logs" }

func TestIntegrationConsumptionCutoverMigration(t *testing.T) {
	drivers := map[string]gorm.Dialector{"sqlite": sqlite.Open(":memory:")}
	if dsn := os.Getenv("SPENDING_TEST_POSTGRES_DSN"); dsn != "" {
		drivers["postgres"] = postgres.Open(dsn)
	}
	if dsn := os.Getenv("SPENDING_TEST_MYSQL_DSN"); dsn != "" {
		drivers["mysql"] = mysql.Open(dsn)
	}
	for name, driver := range drivers {
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sql, _ := db.DB()
			defer sql.Close()
			// The configured PostgreSQL database must be an empty, task-owned test database.
			require.False(t, db.Migrator().HasTable(&Log{}))
			require.NoError(t, db.AutoMigrate(&preScopeLog{}))
			require.NoError(t, db.Create(&preScopeLog{Id: 1, UserId: 7, CreatedAt: 1700000000, Type: LogTypeConsume, Quota: 50}).Error)
			require.NoError(t, db.AutoMigrate(&Log{}))
			require.NoError(t, MigrateIntegrationConsumption(db))
			course := "a134df19-ad21-4fe9-935a-6fcc8b3336b1"
			for _, row := range []Log{
				{Id: 2, UserId: 7, CreatedAt: 1700000001, Type: LogTypeConsume, Quota: 20, BillingScope: course},
				{Id: 3, UserId: 7, CreatedAt: 1700000002, Type: LogTypeRefund, Quota: 5, BillingScope: course},
				{Id: 4, UserId: 7, CreatedAt: 1700000003, Type: LogTypeConsume, Quota: 8}, // old binary after cutover
				{Id: 5, UserId: 8, CreatedAt: 1700000003, Type: LogTypeConsume, Quota: 999, BillingScope: course},
				{Id: 6, UserId: 7, CreatedAt: 1700000003, Type: LogTypeTopup, Quota: 999},
			} {
				require.NoError(t, db.Create(&row).Error)
			}
			require.NoError(t, MigrateIntegrationConsumption(db))
			old := LOG_DB
			LOG_DB = db
			defer func() { LOG_DB = old }()
			summary, err := GetIntegrationConsumptionSummary(7)
			require.NoError(t, err)
			require.EqualValues(t, 4, summary.RecordCount)
			require.EqualValues(t, 73, summary.NetQuota)
			require.Len(t, summary.Groups, 3)
			require.Equal(t, "", summary.Groups[0].BillingScope)
			require.EqualValues(t, 50, summary.Groups[0].NetQuota)
			require.Equal(t, course, summary.Groups[1].BillingScope)
			require.EqualValues(t, 15, summary.Groups[1].NetQuota)
			require.Equal(t, "general", summary.Groups[2].BillingScope)
			require.EqualValues(t, 8, summary.Groups[2].NetQuota)
		})
	}
}
