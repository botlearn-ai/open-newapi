// Package migration applies only the reviewed integration-billing SQL manifest.
// It does not initialize accounts, start the API, or run GORM AutoMigrate.
package migration

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed sql/*.sql
var files embed.FS

const version = "20261014_0001_integration_billing"
const ledgerTable = "newapi_schema_migrations"

type ledgerRow struct {
	Version  string
	Checksum string
}

type integrationAccountV1 struct {
	BillingSuspended bool   `gorm:"not null;default:false"`
	BillingProtocol  string `gorm:"type:varchar(32);not null;default:''"`
}

func (integrationAccountV1) TableName() string { return "integration_accounts" }

// Frozen version-1 table shape. GORM owns dialect-specific primary-key syntax.
// Future schema changes add a new migration instead of editing this snapshot.
type integrationRequestV1 struct {
	Id                int     `gorm:"primaryKey;autoIncrement"`
	AccountId         int     `gorm:"not null"`
	RequestKey        string  `gorm:"type:varchar(128);not null"`
	Path              string  `gorm:"type:varchar(64);not null"`
	BodySHA256        string  `gorm:"type:char(64);not null"`
	MaxQuota          int     `gorm:"not null"`
	QuotaPerUsd       float64 `gorm:"not null"`
	State             string  `gorm:"type:varchar(16);not null"`
	ProviderRequestId string  `gorm:"type:varchar(64);not null"`
	ActualQuota       int     `gorm:"not null;default:0"`
	ChargedQuota      int     `gorm:"not null;default:0"`
	CreatedTime       int64   `gorm:"not null"`
	SettledTime       int64   `gorm:"not null;default:0"`
}

func (integrationRequestV1) TableName() string { return "integration_requests" }

type schemaLogger struct {
	logger.Interface
	statements []string
}

func (l *schemaLogger) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	l.statements = append(l.statements, statement)
}

func manifest(db *gorm.DB) (string, error) {
	asset, err := files.ReadFile("sql/" + db.Dialector.Name() + ".sql")
	if err != nil {
		return "", errors.New("unsupported migration dialect")
	}
	trace := &schemaLogger{Interface: logger.Default.LogMode(logger.Silent)}
	if err := db.Session(&gorm.Session{DryRun: true, Logger: trace}).Migrator().CreateTable(&integrationRequestV1{}); err != nil {
		return "", err
	}
	if len(trace.statements) != 1 {
		return "", errors.New("unexpected generated table manifest")
	}
	return strings.ReplaceAll(string(asset), "{{integration_requests_table}}", trace.statements[0]), nil
}

// Open requires an explicit database; it never falls back to a local SQLite file.
func Open(dsn string) (*gorm.DB, error) {
	var dialect gorm.Dialector
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		dialect = postgres.Open(dsn)
	case strings.HasPrefix(dsn, "sqlite://") && len(dsn) > len("sqlite://"):
		dialect = sqlite.Open(strings.TrimPrefix(dsn, "sqlite://"))
	case strings.Contains(dsn, "@tcp("):
		dialect = mysql.Open(dsn)
	default:
		return nil, errors.New("explicit supported database is required")
	}
	return gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}

// Run holds a database lock and records success only after checking the schema.
// PostgreSQL/SQLite DDL is transactional. MySQL DDL is resumable under GET_LOCK,
// because MySQL commits DDL implicitly; each reviewed statement is checked first.
func Run(ctx context.Context, db *gorm.DB) error {
	dialect := db.Dialector.Name()
	sql, err := manifest(db)
	if err != nil {
		return errors.New("unsupported migration dialect")
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(sql)))
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		// Keep the pinned connection, with a fresh statement for every operation.
		conn = conn.Session(&gorm.Session{NewDB: true})
		if dialect == "mysql" {
			var acquired int
			if err := conn.Raw("SELECT GET_LOCK('newapi-schema-migrations', 30)").Scan(&acquired).Error; err != nil || acquired != 1 {
				return errors.New("migration lock unavailable")
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn.WithContext(cleanup).Exec("SELECT RELEASE_LOCK('newapi-schema-migrations')")
			}()
			return apply(conn, string(sql), checksum)
		}
		return conn.Transaction(func(tx *gorm.DB) error {
			if dialect == "postgres" {
				if err := tx.Exec("SET LOCAL lock_timeout = '30s'").Error; err != nil {
					return err
				}
				if err := tx.Exec("SET LOCAL statement_timeout = '180s'").Error; err != nil {
					return err
				}
				if err := tx.Exec("SELECT pg_advisory_xact_lock(6284921041281)").Error; err != nil {
					return err
				}
			}
			return apply(tx, string(sql), checksum)
		})
	})
}

func apply(db *gorm.DB, sql, checksum string) error {
	for _, table := range []string{"users", "tokens", "integration_accounts", "integration_operations"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("required baseline table missing: %s", table)
		}
	}
	if err := db.Exec("CREATE TABLE IF NOT EXISTS " + ledgerTable + " (version varchar(128) PRIMARY KEY, checksum char(64) NOT NULL, applied_at bigint NOT NULL)").Error; err != nil {
		return err
	}
	// Obtain SQLite's write lock before inspecting migration history.
	if db.Dialector.Name() == "sqlite" {
		if err := db.Exec("UPDATE " + ledgerTable + " SET applied_at = applied_at WHERE 1 = 0").Error; err != nil {
			return err
		}
	}
	var history []ledgerRow
	if err := db.Table(ledgerTable).Select("version, checksum").Find(&history).Error; err != nil {
		return err
	}
	if len(history) > 1 || (len(history) == 1 && (history[0].Version != version || history[0].Checksum != checksum)) {
		return errors.New("migration history does not match bundled manifest")
	}
	if len(history) == 0 {
		statements := strings.Split(sql, ";")
		if len(statements) != 6 {
			return errors.New("invalid bundled migration manifest")
		}
		exists := []bool{
			db.Migrator().HasColumn(&integrationAccountV1{}, "billing_suspended"),
			db.Migrator().HasColumn(&integrationAccountV1{}, "billing_protocol"),
			db.Migrator().HasTable("integration_requests"),
			db.Migrator().HasIndex("integration_requests", "idx_integration_request_key"),
			db.Migrator().HasIndex("integration_requests", "idx_integration_requests_state"),
		}
		for i, present := range exists {
			if !present {
				if err := db.Exec(strings.TrimSpace(statements[i])).Error; err != nil {
					return fmt.Errorf("migration statement %d: %w", i+1, err)
				}
			}
		}
	}
	if err := verifySchema(db); err != nil {
		return err
	}
	if len(history) == 0 {
		if err := db.Exec("INSERT INTO "+ledgerTable+" (version, checksum, applied_at) VALUES (?, ?, ?)", version, checksum, time.Now().Unix()).Error; err != nil {
			return err
		}
	}
	var final []ledgerRow
	if err := db.Table(ledgerTable).Select("version, checksum").Find(&final).Error; err != nil {
		return err
	}
	if len(final) != 1 || final[0].Version != version || final[0].Checksum != checksum {
		return errors.New("final migration manifest mismatch")
	}
	return nil
}

type columnSpec struct {
	kind         string
	length       int64
	defaultValue *string
}

func value(s string) *string { return &s }

func verifySchema(db *gorm.DB) error {
	account := map[string]columnSpec{
		"billing_suspended": {kind: "bool", defaultValue: value("false")},
		"billing_protocol":  {kind: "string", length: 32, defaultValue: value("")},
	}
	request := map[string]columnSpec{
		"id": {kind: "integer"}, "account_id": {kind: "integer"},
		"request_key": {kind: "string", length: 128}, "path": {kind: "string", length: 64},
		"body_sha256": {kind: "string", length: 64}, "max_quota": {kind: "integer"},
		"quota_per_usd": {kind: "float"}, "state": {kind: "string", length: 16},
		"provider_request_id": {kind: "string", length: 64},
		"actual_quota":        {kind: "integer", defaultValue: value("0")},
		"charged_quota":       {kind: "integer", defaultValue: value("0")},
		"created_time":        {kind: "integer"}, "settled_time": {kind: "integer", defaultValue: value("0")},
	}
	for table, expected := range map[string]map[string]columnSpec{"integration_accounts": account, "integration_requests": request} {
		columns, err := db.Migrator().ColumnTypes(table)
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for _, column := range columns {
			spec, tracked := expected[column.Name()]
			if !tracked {
				continue
			}
			found[column.Name()] = true
			kind := strings.ToLower(column.DatabaseTypeName())
			valid := false
			switch spec.kind {
			case "integer":
				valid = strings.Contains(kind, "int")
			case "bool":
				valid = kind == "bool" || kind == "boolean" || kind == "tinyint" || kind == "integer" || kind == "numeric"
			case "float":
				valid = kind == "float8" || kind == "double precision" || kind == "double" || kind == "real" || kind == "numeric" || kind == "decimal"
			case "string":
				valid = kind == "varchar" || kind == "char" || kind == "bpchar" || kind == "text" || kind == "character varying" || kind == "character"
			}
			if !valid {
				return fmt.Errorf("unexpected column type: %s.%s", table, column.Name())
			}
			if column.Name() == "id" {
				if primary, ok := column.PrimaryKey(); !ok || !primary {
					return errors.New("integration request primary key mismatch")
				}
			}
			if nullable, ok := column.Nullable(); ok && nullable && column.Name() != "id" {
				return fmt.Errorf("nullable required column: %s.%s", table, column.Name())
			}
			if spec.length > 0 && db.Dialector.Name() != "sqlite" {
				if length, ok := column.Length(); !ok || length != spec.length {
					return fmt.Errorf("unexpected column length: %s.%s", table, column.Name())
				}
			}
			if spec.defaultValue != nil {
				got, ok := column.DefaultValue()
				got = strings.Trim(strings.Split(got, "::")[0], "'\"()")
				if got == "0" && spec.kind == "bool" {
					got = "false"
				}
				if !ok || got != *spec.defaultValue {
					return fmt.Errorf("unexpected column default: %s.%s", table, column.Name())
				}
			}
		}
		if len(found) != len(expected) {
			return fmt.Errorf("required columns missing: %s", table)
		}
	}
	return verifyIndexes(db)
}

func verifyIndexes(db *gorm.DB) error {
	if db.Dialector.Name() == "sqlite" {
		var list []struct {
			Name   string
			Unique int
		}
		if err := db.Raw("PRAGMA index_list('integration_requests')").Scan(&list).Error; err != nil {
			return err
		}
		for _, want := range []struct {
			name    string
			columns string
			unique  int
		}{{"idx_integration_request_key", "account_id,request_key", 1}, {"idx_integration_requests_state", "state", 0}} {
			found := false
			for _, idx := range list {
				if idx.Name != want.name || idx.Unique != want.unique {
					continue
				}
				var cols []struct{ Name string }
				if err := db.Raw("PRAGMA index_info('" + want.name + "')").Scan(&cols).Error; err != nil {
					return err
				}
				names := []string{}
				for _, c := range cols {
					names = append(names, c.Name)
				}
				found = strings.Join(names, ",") == want.columns
			}
			if !found {
				return errors.New("required index shape mismatch")
			}
		}
		return nil
	}
	indexes, err := db.Migrator().GetIndexes("integration_requests")
	if err != nil {
		return err
	}
	for _, want := range []struct {
		name    string
		columns string
		unique  bool
	}{{"idx_integration_request_key", "account_id,request_key", true}, {"idx_integration_requests_state", "state", false}} {
		found := false
		for _, idx := range indexes {
			unique, ok := idx.Unique()
			if idx.Name() == want.name && ok && unique == want.unique && strings.Join(idx.Columns(), ",") == want.columns {
				found = true
			}
		}
		if !found {
			return errors.New("required index shape mismatch")
		}
	}
	return nil
}
