package dao

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SafeSyncImportTableReport struct {
	Table           string `json:"table"`
	SourceCount     int64  `json:"sourceCount"`
	TargetCount     int64  `json:"targetCount"`
	SourceMaxID     int64  `json:"sourceMaxId"`
	TargetMaxID     int64  `json:"targetMaxId"`
	SourceKeyDigest string `json:"sourceKeyDigest,omitempty"`
	TargetKeyDigest string `json:"targetKeyDigest,omitempty"`
}

type SafeSyncImportReport struct {
	UID                int64                       `json:"uid"`
	DryRun             bool                        `json:"dryRun"`
	Verified           bool                        `json:"verified"`
	VerificationDigest string                      `json:"verificationDigest,omitempty"`
	Tables             []SafeSyncImportTableReport `json:"tables"`
}

func (r *SafeSyncImportReport) Table(name string) SafeSyncImportTableReport {
	if r == nil {
		return SafeSyncImportTableReport{Table: name}
	}
	for _, table := range r.Tables {
		if table.Table == name {
			return table
		}
	}
	return SafeSyncImportTableReport{Table: name}
}

type SafeSyncPostgresImporter struct {
	source *Dao
	target *Dao
}

func NewSafeSyncPostgresImporter(source, target *Dao) (*SafeSyncPostgresImporter, error) {
	if source == nil || target == nil {
		return nil, errors.New("source and target dao are required")
	}
	if source.resolveConfig("user_1").Type != "sqlite" {
		return nil, errors.New("safe sync import source must use sqlite user databases")
	}
	if target.resolveConfig("user_1").Type != "postgres" {
		return nil, errors.New("safe sync import target must use postgres user databases")
	}
	return newSafeSyncPostgresImporter(source, target), nil
}

func newSafeSyncPostgresImporter(source, target *Dao) *SafeSyncPostgresImporter {
	return &SafeSyncPostgresImporter{source: source, target: target}
}

type safeSyncImportSpec struct {
	modelKey   string
	table      string
	keyColumns []string
	newRows    func() any
}

type safeSyncImportSnapshot struct {
	spec   safeSyncImportSpec
	rows   any
	report SafeSyncImportTableReport
}

func (i *SafeSyncPostgresImporter) ImportUser(ctx context.Context, uid int64, dryRun bool) (*SafeSyncImportReport, error) {
	if i == nil || i.source == nil || i.target == nil {
		return nil, errors.New("safe sync postgres importer is not initialized")
	}
	if uid <= 0 {
		return nil, errors.New("uid must be positive")
	}

	snapshots := make([]safeSyncImportSnapshot, 0, len(safeSyncImportSpecs()))
	report := &SafeSyncImportReport{UID: uid, DryRun: dryRun}
	for _, spec := range safeSyncImportSpecs() {
		snapshot, err := i.readSourceSnapshot(ctx, uid, spec)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
		report.Tables = append(report.Tables, snapshot.report)
	}
	if dryRun {
		return report, nil
	}

	targetDB := i.target.ResolveDB(NewSafeSyncUnitOfWork(i.target).GetKey(uid))
	if targetDB == nil {
		return nil, errors.New("postgres target user database is unavailable")
	}
	var verificationDigest string
	err := targetDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, snapshot := range snapshots {
			if err := model.AutoMigrate(tx, snapshot.spec.modelKey); err != nil {
				return fmt.Errorf("migrate target table %s: %w", snapshot.spec.table, err)
			}
			if reflect.ValueOf(snapshot.rows).Elem().Len() > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(snapshot.rows, 200).Error; err != nil {
					return fmt.Errorf("import target table %s: %w", snapshot.spec.table, err)
				}
			}
			if err := syncSafeSyncImportSequence(tx, snapshot.spec.table); err != nil {
				return err
			}
		}

		for index := range snapshots {
			targetStats, err := inspectSafeSyncImportTable(tx, snapshots[index].spec)
			if err != nil {
				return err
			}
			report.Tables[index].TargetCount = targetStats.SourceCount
			report.Tables[index].TargetMaxID = targetStats.SourceMaxID
			report.Tables[index].TargetKeyDigest = targetStats.SourceKeyDigest
			if err := validateSafeSyncImportTable(report.Tables[index]); err != nil {
				return err
			}
		}

		if err := model.AutoMigrateSafeSync(tx); err != nil {
			return fmt.Errorf("migrate safe sync metadata: %w", err)
		}
		verifiedAt := time.Now().UTC()
		if err := backfillResourceMetadata(tx, &verifiedAt); err != nil {
			return fmt.Errorf("backfill verified safe sync metadata: %w", err)
		}
		verificationDigest = safeSyncImportReportDigest(report)
		verification := model.SafeSyncMigrationState{
			ID:           1,
			Status:       "VERIFIED",
			ReportDigest: verificationDigest,
			VerifiedAt:   &verifiedAt,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"status", "report_digest", "verified_at", "updated_at",
			}),
		}).Create(&verification).Error; err != nil {
			return fmt.Errorf("persist safe sync migration verification: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	report.Verified = true
	report.VerificationDigest = verificationDigest
	return report, nil
}

func safeSyncImportReportDigest(report *SafeSyncImportReport) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "uid=%d\x1e", report.UID)
	for _, table := range report.Tables {
		_, _ = fmt.Fprintf(hash, "%s\x1f%d\x1f%d\x1f%d\x1f%d\x1f%s\x1f%s\x1e",
			table.Table,
			table.SourceCount,
			table.TargetCount,
			table.SourceMaxID,
			table.TargetMaxID,
			table.SourceKeyDigest,
			table.TargetKeyDigest,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func syncSafeSyncImportSequence(db *gorm.DB, table string) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}

	var sequence sql.NullString
	if err := db.Raw("SELECT pg_get_serial_sequence(?, 'id')", table).Scan(&sequence).Error; err != nil {
		return fmt.Errorf("resolve sequence for table %s: %w", table, err)
	}
	if !sequence.Valid || sequence.String == "" {
		return nil
	}

	var maxID int64
	if err := db.Table(table).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
		return fmt.Errorf("max id for sequence on table %s: %w", table, err)
	}
	if maxID == 0 {
		if err := db.Exec("SELECT setval(?::regclass, 1, false)", sequence.String).Error; err != nil {
			return fmt.Errorf("reset empty sequence for table %s: %w", table, err)
		}
		return nil
	}
	if err := db.Exec("SELECT setval(?::regclass, ?, true)", sequence.String, maxID).Error; err != nil {
		return fmt.Errorf("advance sequence for table %s: %w", table, err)
	}
	return nil
}

func (i *SafeSyncPostgresImporter) readSourceSnapshot(ctx context.Context, uid int64, spec safeSyncImportSpec) (safeSyncImportSnapshot, error) {
	rows := spec.newRows()
	snapshot := safeSyncImportSnapshot{
		spec: spec,
		rows: rows,
		report: SafeSyncImportTableReport{
			Table: spec.table,
		},
	}
	modelConfig, ok := findModelConfig(spec.modelKey)
	if !ok || modelConfig.RepoFactory == nil || modelConfig.IsMainDB {
		return snapshot, fmt.Errorf("user model routing is unavailable for %s", spec.modelKey)
	}
	sourceDB := i.source.ResolveDB(modelConfig.RepoFactory(i.source).GetKey(uid))
	if sourceDB == nil {
		return snapshot, fmt.Errorf("source user database is unavailable for %s", spec.modelKey)
	}
	if sourceDB.Migrator().HasTable(spec.table) {
		if err := sourceDB.WithContext(ctx).Table(spec.table).Order("id").Find(rows).Error; err != nil {
			return snapshot, fmt.Errorf("read source table %s: %w", spec.table, err)
		}
	}
	stats, err := inspectSafeSyncImportTable(sourceDB.WithContext(ctx), spec)
	if err != nil {
		return snapshot, err
	}
	snapshot.report = stats
	return snapshot, nil
}

func findModelConfig(name string) (ModelConfig, bool) {
	for _, cfg := range modelConfigs {
		if cfg.Name == name {
			return cfg, true
		}
	}
	return ModelConfig{}, false
}

func inspectSafeSyncImportTable(db *gorm.DB, spec safeSyncImportSpec) (SafeSyncImportTableReport, error) {
	report := SafeSyncImportTableReport{Table: spec.table}
	if len(spec.keyColumns) > 0 {
		emptyDigest := sha256.Sum256(nil)
		report.SourceKeyDigest = hex.EncodeToString(emptyDigest[:])
	}
	if !db.Migrator().HasTable(spec.table) {
		return report, nil
	}
	if err := db.Table(spec.table).Count(&report.SourceCount).Error; err != nil {
		return report, fmt.Errorf("count table %s: %w", spec.table, err)
	}
	if report.SourceCount > 0 {
		if err := db.Table(spec.table).Select("COALESCE(MAX(id), 0)").Scan(&report.SourceMaxID).Error; err != nil {
			return report, fmt.Errorf("max id for table %s: %w", spec.table, err)
		}
	}
	if len(spec.keyColumns) > 0 {
		digest, err := safeSyncKeyDigest(db, spec.table, spec.keyColumns)
		if err != nil {
			return report, err
		}
		report.SourceKeyDigest = digest
	}
	return report, nil
}

func safeSyncKeyDigest(db *gorm.DB, table string, columns []string) (string, error) {
	rows, err := db.Table(table).Select(strings.Join(columns, ", ")).Order("id").Rows()
	if err != nil {
		return "", fmt.Errorf("read key digest for table %s: %w", table, err)
	}
	defer rows.Close()

	hash := sha256.New()
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return "", err
		}
		for _, value := range values {
			switch typed := value.(type) {
			case []byte:
				_, _ = fmt.Fprintf(hash, "%s\x1f", string(typed))
			default:
				_, _ = fmt.Fprintf(hash, "%v\x1f", typed)
			}
		}
		_, _ = hash.Write([]byte{0x1e})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateSafeSyncImportTable(report SafeSyncImportTableReport) error {
	if report.SourceCount != report.TargetCount || report.SourceMaxID != report.TargetMaxID || report.SourceKeyDigest != report.TargetKeyDigest {
		return fmt.Errorf("safe sync import verification failed for %s: source(count=%d,max=%d,digest=%s) target(count=%d,max=%d,digest=%s)",
			report.Table,
			report.SourceCount, report.SourceMaxID, report.SourceKeyDigest,
			report.TargetCount, report.TargetMaxID, report.TargetKeyDigest,
		)
	}
	return nil
}

func safeSyncImportSpecs() []safeSyncImportSpec {
	return []safeSyncImportSpec{
		{modelKey: "BackupConfig", table: model.TableNameBackupConfig, newRows: func() any { return &[]model.BackupConfig{} }},
		{modelKey: "BackupHistory", table: model.TableNameBackupHistory, newRows: func() any { return &[]model.BackupHistory{} }},
		{modelKey: "File", table: model.TableNameFile, keyColumns: []string{"id", "vault_id", "action", "path", "path_hash", "content_hash", "size", "fid", "rename"}, newRows: func() any { return &[]model.File{} }},
		{modelKey: "Folder", table: model.TableNameFolder, keyColumns: []string{"id", "vault_id", "action", "path", "path_hash", "level", "fid"}, newRows: func() any { return &[]model.Folder{} }},
		{modelKey: "GitSyncConfig", table: model.TableNameGitSyncConfig, newRows: func() any { return &[]model.GitSyncConfig{} }},
		{modelKey: "GitSyncHistory", table: model.TableNameGitSyncHistory, newRows: func() any { return &[]model.GitSyncHistory{} }},
		{modelKey: "Note", table: model.TableNameNote, keyColumns: []string{"id", "vault_id", "action", "path", "path_hash", "content_hash", "size", "version", "fid", "rename"}, newRows: func() any { return &[]model.Note{} }},
		{modelKey: "NoteHistory", table: model.TableNameNoteHistory, newRows: func() any { return &[]model.NoteHistory{} }},
		{modelKey: "NoteLink", table: model.TableNameNoteLink, newRows: func() any { return &[]model.NoteLink{} }},
		{modelKey: "Setting", table: model.TableNameSetting, newRows: func() any { return &[]model.Setting{} }},
		{modelKey: "Storage", table: model.TableNameStorage, newRows: func() any { return &[]model.Storage{} }},
		{modelKey: "SyncLog", table: model.TableNameSyncLog, newRows: func() any { return &[]model.SyncLog{} }},
		{modelKey: "UserShare", table: model.TableNameUserShare, newRows: func() any { return &[]model.UserShare{} }},
		{modelKey: "Vault", table: model.TableNameVault, keyColumns: []string{"id", "vault", "is_deleted", "note_count", "note_size", "file_count", "file_size"}, newRows: func() any { return &[]model.Vault{} }},
	}
}
