package migration

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManifestMatchesBillingRequestModel(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		live := &gorm.Statement{DB: db}
		frozen := &gorm.Statement{DB: db}
		require.NoError(t, live.Parse(&model.IntegrationRequest{}))
		require.NoError(t, frozen.Parse(&integrationRequestV1{}))
		require.Equal(t, live.Schema.DBNames, frozen.Schema.DBNames)
		for _, name := range live.Schema.DBNames {
			actual := live.Schema.FieldsByDBName[name]
			planned := frozen.Schema.FieldsByDBName[name]
			require.Equal(t, db.Migrator().FullDataTypeOf(actual).SQL, db.Migrator().FullDataTypeOf(planned).SQL, name)
			require.Equal(t, actual.PrimaryKey, planned.PrimaryKey, name)
			require.Equal(t, actual.AutoIncrement, planned.AutoIncrement, name)
		}
	})
}

type legacyAccount struct {
	Id              int
	IntegrationId   string `gorm:"type:varchar(64);not null;uniqueIndex:idx_integration_external,priority:1"`
	ExternalUserKey string `gorm:"type:char(64);not null;uniqueIndex:idx_integration_external,priority:2"`
	ExternalUserId  string `gorm:"type:text;not null"`
	UserId          int    `gorm:"not null;index"`
	TokenId         int    `gorm:"not null;index"`
	CreatedTime     int64  `gorm:"not null"`
}

func (legacyAccount) TableName() string { return "integration_accounts" }

func databases(t *testing.T, test func(*testing.T, *gorm.DB)) {
	t.Helper()
	for _, name := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(name, func(t *testing.T) {
			dsn := "sqlite://file:" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared"
			if name != "sqlite" {
				dsn = os.Getenv("TEST_MIGRATION_" + strings.ToUpper(name) + "_DSN")
				if dsn == "" {
					t.Skip("isolated database not configured")
				}
				// Destructive fixture setup is confined to this explicit local test DB.
				if name == "postgres" {
					parsed, err := url.Parse(dsn)
					require.NoError(t, err)
					require.Equal(t, "127.0.0.1", parsed.Hostname())
					require.Equal(t, "/newapi_migration_test", parsed.Path)
					require.NotContains(t, parsed.Query(), "host")
					require.NotContains(t, parsed.Query(), "dbname")
				} else {
					parsed, err := mysqldriver.ParseDSN(dsn)
					require.NoError(t, err)
					host, _, err := net.SplitHostPort(parsed.Addr)
					require.NoError(t, err)
					require.Equal(t, "tcp", parsed.Net)
					require.Equal(t, "127.0.0.1", host)
					require.Equal(t, "newapi_migration_test", parsed.DBName)
				}
			}
			db, err := Open(dsn)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			for _, table := range []string{ledgerTable, "integration_requests", "integration_accounts", "integration_operations", "users", "tokens"} {
				require.NoError(t, db.Migrator().DropTable(table))
			}
			test(t, db)
		})
	}
}

func baseline(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&legacyAccount{}))
	for _, table := range []string{"users", "tokens", "integration_operations"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id bigint PRIMARY KEY, quota bigint NOT NULL)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (id, quota) VALUES (1, 17489582)").Error)
	}
	require.NoError(t, db.Create(&legacyAccount{Id: 1, IntegrationId: "course", ExternalUserKey: strings.Repeat("a", 64), ExternalUserId: "qa", UserId: 1, TokenId: 1, CreatedTime: 1234}).Error)
}

func TestMigrationPreservesDataAndIsIdempotent(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		baseline(t, db)
		for i := 0; i < 2; i++ {
			require.NoError(t, Run(context.Background(), db))
		}
		for _, table := range []string{"users", "tokens", "integration_operations"} {
			var quota int64
			require.NoError(t, db.Table(table).Select("quota").Scan(&quota).Error)
			require.EqualValues(t, 17489582, quota)
		}
		var account struct {
			BillingSuspended bool
			BillingProtocol  string
			ExternalUserId   string
			CreatedTime      int64
		}
		require.NoError(t, db.Table("integration_accounts").First(&account).Error)
		require.False(t, account.BillingSuspended)
		require.Empty(t, account.BillingProtocol)
		require.Equal(t, "qa", account.ExternalUserId)
		require.EqualValues(t, 1234, account.CreatedTime)
		var count int64
		require.NoError(t, db.Table(ledgerTable).Count(&count).Error)
		require.EqualValues(t, 1, count)
		insert := "INSERT INTO integration_requests (account_id, request_key, path, body_sha256, max_quota, quota_per_usd, state, provider_request_id, created_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
		args := []any{1, "req-1", "/v1/chat/completions", strings.Repeat("b", 64), 100, 500000, "authorized", "provider-1", 12345}
		require.NoError(t, db.Exec(insert, args...).Error)
		require.Error(t, db.Exec(insert, args...).Error, "unique billing key must be enforced")
		require.NoError(t, db.Table("integration_accounts").Where("id = ?", 1).Updates(map[string]any{"billing_suspended": true, "billing_protocol": "credits-v1"}).Error)
		require.NoError(t, Run(context.Background(), db))
		require.NoError(t, db.Table("integration_accounts").First(&account).Error)
		require.True(t, account.BillingSuspended)
		require.Equal(t, "credits-v1", account.BillingProtocol)
		require.NoError(t, db.Table("integration_requests").Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}

func TestMigrationRejectsUnknownOrChangedHistory(t *testing.T) {
	for _, corrupt := range []string{"version", "checksum"} {
		t.Run(corrupt, func(t *testing.T) {
			databases(t, func(t *testing.T, db *gorm.DB) {
				baseline(t, db)
				require.NoError(t, Run(context.Background(), db))
				require.NoError(t, db.Exec("UPDATE "+ledgerTable+" SET "+corrupt+" = ?", "unknown").Error)
				require.ErrorContains(t, Run(context.Background(), db), "history does not match")
			})
		})
	}
}

func TestMigrationRejectsSchemaDriftAndDoesNotRecordSuccess(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		baseline(t, db)
		require.NoError(t, db.Exec("ALTER TABLE integration_accounts ADD COLUMN billing_protocol integer NOT NULL DEFAULT 0").Error)
		require.ErrorContains(t, Run(context.Background(), db), "unexpected column type")
		if db.Migrator().HasTable(ledgerTable) {
			var count int64
			require.NoError(t, db.Table(ledgerTable).Count(&count).Error)
			require.Zero(t, count)
		}
	})
}

func TestMigrationRejectsMissingBaseline(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		require.ErrorContains(t, Run(context.Background(), db), "baseline table missing")
		require.False(t, db.Migrator().HasTable(ledgerTable))
	})
}

func TestMigrationRejectsIndexDrift(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		baseline(t, db)
		require.NoError(t, Run(context.Background(), db))
		require.NoError(t, db.Migrator().DropIndex("integration_requests", "idx_integration_request_key"))
		require.NoError(t, db.Exec("CREATE INDEX idx_integration_request_key ON integration_requests(account_id, request_key)").Error)
		require.ErrorContains(t, Run(context.Background(), db), "index shape mismatch")
	})
}

func TestMigrationConcurrentServers(t *testing.T) {
	databases(t, func(t *testing.T, db *gorm.DB) {
		if db.Dialector.Name() == "sqlite" {
			t.Skip("SQLite concurrent writers return busy; server lock test uses PostgreSQL and MySQL")
		}
		baseline(t, db)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- Run(context.Background(), db) }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		var count int64
		require.NoError(t, db.Table(ledgerTable).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}

func TestOpenRequiresExplicitDatabase(t *testing.T) {
	for _, dsn := range []string{"", "local", "sqlite://", "https://example.test/db"} {
		t.Run(fmt.Sprintf("%q", dsn), func(t *testing.T) { _, err := Open(dsn); require.Error(t, err) })
	}
}
