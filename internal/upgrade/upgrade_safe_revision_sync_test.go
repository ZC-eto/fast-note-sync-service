package upgrade

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/haierkeys/fast-note-sync-service/pkg/writequeue"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSafeRevisionSyncMigrate_PostgresGateAndIdempotentBackfill(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration.sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath, EnableWriteQueue: util.Ptr(true)}
	migrationManager := NewMigrationManager(mainDB, zap.NewNop(), "3.6.2", cfg, cfg)
	migrationWriteQueue := writequeue.New(nil, zap.NewNop())
	d := migrationManager.newMigrationDao(ctx, migrationWriteQueue)
	t.Cleanup(func() {
		require.NoError(t, migrationWriteQueue.Shutdown(context.Background()))
		for _, key := range []string{"user_safe_sync_1"} {
			if sqlDB, closeErr := d.ResolveDB(key).DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
		}
		if sqlDB, closeErr := mainDB.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, mainDB.AutoMigrate(&model.User{}))
	require.NoError(t, mainDB.Create(&model.User{UID: 1, Username: "zebra"}).Error)
	uow := dao.NewSafeSyncUnitOfWork(d)
	userDB := d.ResolveDB(uow.GetKey(1))
	require.NoError(t, userDB.AutoMigrate(&model.Vault{}, &model.Note{}))
	require.NoError(t, userDB.Create(&model.Vault{ID: 5, Vault: "Personal"}).Error)
	require.NoError(t, userDB.Create(&model.Note{ID: 9, VaultID: 5, Action: "modify", Path: "a.md", PathHash: "path-a", ContentHash: "hash-a"}).Error)

	migration := &SafeRevisionSyncMigrate{}
	require.Equal(t, "3.6.1", migration.Version())

	// SQLite user databases stay unsupported and must not receive safe-sync state.
	require.NoError(t, migration.Up(mainDB, ctx, &MigrationContext{
		Logger:           zap.NewNop(),
		Dao:              d,
		UserDatabaseType: "sqlite",
	}))
	require.False(t, userDB.Migrator().HasTable(&model.VaultSyncState{}))

	for range 2 {
		require.NoError(t, migration.Up(mainDB, ctx, &MigrationContext{
			Logger:           zap.NewNop(),
			Dao:              d,
			UserDatabaseType: "postgres",
		}))
	}

	var resources []model.SyncResourceMetadata
	require.NoError(t, userDB.Find(&resources).Error)
	require.Len(t, resources, 1)
	require.Equal(t, int64(1), resources[0].ResourceRevision)
	var state model.VaultSyncState
	require.NoError(t, userDB.Where("vault_id = ?", 5).First(&state).Error)
	require.Equal(t, "OFF", state.State)
	require.Nil(t, state.MigrationVerifiedAt, "schema migration must not claim that the SQLite import was verified")
	verified, err := uow.IsMigrationVerified(ctx, 1)
	require.NoError(t, err)
	require.False(t, verified)
}
