package dao

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSafeSyncImportDao(t *testing.T, name string) *Dao {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), name+".sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	cfg := &config.DatabaseConfig{
		Type:             "sqlite",
		Path:             dbPath,
		EnableWriteQueue: util.Ptr(false),
	}
	d := New(mainDB, context.Background(),
		WithConfig(cfg),
		WithUserDatabaseConfig(cfg),
		WithLogger(zap.NewNop()),
	)
	t.Cleanup(func() {
		for _, entry := range d.KeyDb {
			if sqlDB, closeErr := entry.db.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
		}
		if sqlDB, closeErr := mainDB.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return d
}

func TestSafeSyncPostgresImporter_DryRunImportAndRetry(t *testing.T) {
	ctx := context.Background()
	const uid = int64(42)
	source := setupSafeSyncImportDao(t, "source")
	target := setupSafeSyncImportDao(t, "target")

	sourceVaultDB := source.ResolveDB("user_vault_42")
	require.NoError(t, sourceVaultDB.AutoMigrate(&model.Vault{}))
	require.NoError(t, sourceVaultDB.Create(&model.Vault{ID: 3, Vault: "Personal", NoteCount: 1, FileCount: 1}).Error)

	sourceNoteDB := source.ResolveDB("user_42")
	require.NoError(t, sourceNoteDB.AutoMigrate(&model.Note{}))
	require.NoError(t, sourceNoteDB.Create(&model.Note{ID: 11, VaultID: 3, Action: "modify", Path: "a.md", PathHash: "path-a", ContentHash: "hash-a", Size: 12}).Error)

	sourceFileDB := source.ResolveDB("user_file_42")
	require.NoError(t, sourceFileDB.AutoMigrate(&model.File{}))
	require.NoError(t, sourceFileDB.Create(&model.File{ID: 12, VaultID: 3, Action: "create", Path: "image.png", PathHash: "path-image", ContentHash: "hash-image", Size: 20}).Error)

	sourceFolderDB := source.ResolveDB("user_folder_42")
	require.NoError(t, sourceFolderDB.AutoMigrate(&model.Folder{}))
	require.NoError(t, sourceFolderDB.Create(&model.Folder{ID: 13, VaultID: 3, Action: "create", Path: "docs", PathHash: "path-docs"}).Error)

	importer := newSafeSyncPostgresImporter(source, target)
	dryRun, err := importer.ImportUser(ctx, uid, true)
	require.NoError(t, err)
	require.True(t, dryRun.DryRun)
	require.False(t, dryRun.Verified)
	require.Equal(t, int64(1), dryRun.Table("note").SourceCount)

	targetDB := target.ResolveDB("user_safe_sync_42")
	require.False(t, targetDB.Migrator().HasTable(&model.Note{}), "dry-run must not create target tables")
	require.False(t, targetDB.Migrator().HasTable(&model.SafeSyncMigrationState{}), "dry-run must not create migration verification")

	first, err := importer.ImportUser(ctx, uid, false)
	require.NoError(t, err)
	require.True(t, first.Verified)
	for _, table := range []string{"vault", "note", "file", "folder"} {
		tableReport := first.Table(table)
		require.Equal(t, int64(1), tableReport.SourceCount, table)
		require.Equal(t, tableReport.SourceCount, tableReport.TargetCount, table)
		require.Equal(t, tableReport.SourceMaxID, tableReport.TargetMaxID, table)
		require.Equal(t, tableReport.SourceKeyDigest, tableReport.TargetKeyDigest, table)
	}

	var note model.Note
	require.NoError(t, targetDB.First(&note, 11).Error)
	require.Equal(t, "a.md", note.Path)
	var file model.File
	require.NoError(t, targetDB.First(&file, 12).Error)
	require.Equal(t, "hash-image", file.ContentHash)
	var resources []model.SyncResourceMetadata
	require.NoError(t, targetDB.Order("resource_type, legacy_id").Find(&resources).Error)
	require.Len(t, resources, 3)
	var vaultState model.VaultSyncState
	require.NoError(t, targetDB.Where("vault_id = ?", 3).First(&vaultState).Error)
	require.NotNil(t, vaultState.MigrationVerifiedAt)
	verified, err := NewSafeSyncUnitOfWork(target).IsMigrationVerified(ctx, uid)
	require.NoError(t, err)
	require.True(t, verified)

	second, err := importer.ImportUser(ctx, uid, false)
	require.NoError(t, err)
	require.True(t, second.Verified)
	for _, table := range []string{"vault", "note", "file", "folder"} {
		require.Equal(t, int64(1), second.Table(table).TargetCount, table)
	}
	require.NoError(t, targetDB.Find(&resources).Error)
	require.Len(t, resources, 3)
}

func TestSafeSyncPostgresImporter_VerifiesEmptyUserImport(t *testing.T) {
	ctx := context.Background()
	const uid = int64(7)
	source := setupSafeSyncImportDao(t, "empty-source")
	target := setupSafeSyncImportDao(t, "empty-target")
	importer := newSafeSyncPostgresImporter(source, target)

	report, err := importer.ImportUser(ctx, uid, false)
	require.NoError(t, err)
	require.True(t, report.Verified)

	verified, err := NewSafeSyncUnitOfWork(target).IsMigrationVerified(ctx, uid)
	require.NoError(t, err)
	require.True(t, verified)
}
