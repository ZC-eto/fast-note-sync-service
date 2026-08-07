package dao

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSafeSyncUnitOfWorkTest(t *testing.T) (*SafeSyncUnitOfWork, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "safe-sync.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{})
	require.NoError(t, err)

	dbCfg := &config.DatabaseConfig{
		Type:             "sqlite",
		Path:             dbPath,
		EnableWriteQueue: util.Ptr(false),
	}
	daoInst := New(db, context.Background(),
		WithConfig(dbCfg),
		WithUserDatabaseConfig(dbCfg),
		WithLogger(zap.NewNop()),
	)
	uow := NewSafeSyncUnitOfWork(daoInst)
	require.NoError(t, uow.Migrate(context.Background(), 1))
	t.Cleanup(func() {
		for _, entry := range daoInst.KeyDb {
			if sqlDB, closeErr := entry.db.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
		}
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return uow, daoInst.ResolveDB(uow.GetKey(1))
}

func TestSafeSyncUnitOfWork_RollsBackResourceEventAndOperationTogether(t *testing.T) {
	uow, db := setupSafeSyncUnitOfWorkTest(t)
	ctx := context.Background()
	wantErr := errors.New("injected transaction failure")

	err := uow.Transaction(ctx, 1, func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&model.VaultSyncState{VaultID: 7, State: "OFF"}).Error)
		require.NoError(t, tx.Create(&model.SyncResourceMetadata{
			ResourceID: "resource-1", VaultID: 7, ResourceType: "NOTE", ResourceRevision: 1,
			CurrentPath: "notes/a.md", CurrentPathHash: "path-a", State: "LIVE",
		}).Error)
		require.NoError(t, tx.Create(&model.SyncEvent{
			VaultID: 7, VaultRevision: 1, ResourceID: "resource-1", ResourceRevision: 1,
			ResourceType: "NOTE", Action: "CREATE", Path: "notes/a.md", OperationID: "operation-1",
		}).Error)
		require.NoError(t, tx.Create(&model.SyncOperation{
			VaultID: 7, DeviceID: "device-1", OperationID: "operation-1",
			RequestFingerprint: "fingerprint-1", State: "COMMITTED",
		}).Error)
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	for _, table := range []any{
		&model.VaultSyncState{},
		&model.SyncResourceMetadata{},
		&model.SyncEvent{},
		&model.SyncOperation{},
	} {
		var count int64
		require.NoError(t, db.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestSyncResourceMetadata_AllowsDeletedHistoryButOnlyOneLivePath(t *testing.T) {
	_, db := setupSafeSyncUnitOfWorkTest(t)
	deleted := model.SyncResourceMetadata{
		ResourceID: "deleted-resource", VaultID: 2, ResourceType: "NOTE", LegacyID: 1,
		ResourceRevision: 2, CurrentPath: "a.md", State: "DELETED",
	}
	live := model.SyncResourceMetadata{
		ResourceID: "live-resource", VaultID: 2, ResourceType: "NOTE", LegacyID: 2,
		ResourceRevision: 1, CurrentPath: "a.md", State: "LIVE",
	}
	secondLive := model.SyncResourceMetadata{
		ResourceID: "second-live-resource", VaultID: 2, ResourceType: "NOTE", LegacyID: 3,
		ResourceRevision: 1, CurrentPath: "a.md", State: "LIVE",
	}
	require.NoError(t, db.Create(&deleted).Error)
	require.NoError(t, db.Create(&live).Error)
	require.Error(t, db.Create(&secondLive).Error)
}

func TestSafeSyncUnitOfWork_AllocatesUniqueMonotonicVaultRevisions(t *testing.T) {
	uow, db := setupSafeSyncUnitOfWorkTest(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 9, State: "STRICT"}).Error)

	const workers = 12
	revisions := make(chan int64, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := uow.Transaction(ctx, 1, func(tx *gorm.DB) error {
				start, end, err := uow.AllocateVaultRevisions(tx, 9, 1)
				if err == nil {
					require.Equal(t, start, end)
					revisions <- end
				}
				return err
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(revisions)

	for err := range errs {
		require.NoError(t, err)
	}
	got := make([]int, 0, workers)
	for revision := range revisions {
		got = append(got, int(revision))
	}
	sort.Ints(got)
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, got)

	var state model.VaultSyncState
	require.NoError(t, db.Where("vault_id = ?", 9).First(&state).Error)
	require.Equal(t, int64(workers), state.LatestVaultRevision)
}

func TestSafeSyncUnitOfWork_PrunesOnlyExpiredTerminalOperations(t *testing.T) {
	uow, db := setupSafeSyncUnitOfWorkTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	operations := []model.SyncOperation{
		{VaultID: 1, DeviceID: "d", OperationID: "old-committed", State: "COMMITTED", ExpiresAt: now.Add(-time.Hour)},
		{VaultID: 1, DeviceID: "d", OperationID: "old-rejected", State: "REJECTED", ExpiresAt: now.Add(-time.Hour)},
		{VaultID: 1, DeviceID: "d", OperationID: "old-prepared", State: "PREPARED", ExpiresAt: now.Add(-time.Hour)},
		{VaultID: 1, DeviceID: "d", OperationID: "recent-committed", State: "COMMITTED", ExpiresAt: now.Add(24 * time.Hour)},
	}
	require.NoError(t, db.Create(&operations).Error)

	deleted, err := uow.PruneTerminalOperations(ctx, 1, now)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	var remaining []model.SyncOperation
	require.NoError(t, db.Order("operation_id").Find(&remaining).Error)
	require.Len(t, remaining, 2)
	require.Equal(t, "old-prepared", remaining[0].OperationID)
	require.Equal(t, "recent-committed", remaining[1].OperationID)
}

func TestSyncOperation_DefaultsToThirtyDayRetention(t *testing.T) {
	_, db := setupSafeSyncUnitOfWorkTest(t)
	before := time.Now().UTC().Add(30 * 24 * time.Hour)
	operation := model.SyncOperation{
		VaultID: 1, DeviceID: "device", OperationID: "operation", RequestFingerprint: "fingerprint", State: "COMMITTED",
	}
	require.NoError(t, db.Create(&operation).Error)
	after := time.Now().UTC().Add(30 * 24 * time.Hour)
	require.False(t, operation.ExpiresAt.Before(before))
	require.False(t, operation.ExpiresAt.After(after))
}

func TestSafeSyncUnitOfWork_BackfillIsIdempotent(t *testing.T) {
	uow, db := setupSafeSyncUnitOfWorkTest(t)
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&model.Vault{}, &model.Note{}, &model.File{}, &model.Folder{}))
	require.NoError(t, db.Create(&model.Vault{ID: 3, Vault: "Personal"}).Error)
	require.NoError(t, db.Create(&model.Note{ID: 11, VaultID: 3, Action: "modify", Path: "a.md", PathHash: "path-a", ContentHash: "hash-a", Size: 12}).Error)
	require.NoError(t, db.Create(&model.File{ID: 12, VaultID: 3, Action: "delete", Path: "image.png", PathHash: "path-image", ContentHash: "hash-image", Size: 20}).Error)
	require.NoError(t, db.Create(&model.Folder{ID: 13, VaultID: 3, Action: "create", Path: "docs", PathHash: "path-docs"}).Error)

	for range 2 {
		require.NoError(t, uow.BackfillResourceMetadata(ctx, 1))
	}

	var resources []model.SyncResourceMetadata
	require.NoError(t, db.Order("resource_type, legacy_id").Find(&resources).Error)
	require.Len(t, resources, 3)
	ids := map[string]struct{}{}
	for _, resource := range resources {
		require.NotEmpty(t, resource.ResourceID)
		require.Equal(t, int64(1), resource.ResourceRevision)
		ids[resource.ResourceID] = struct{}{}
	}
	require.Len(t, ids, 3)
	require.Equal(t, "DELETED", resources[0].State)

	var state model.VaultSyncState
	require.NoError(t, db.Where("vault_id = ?", 3).First(&state).Error)
	require.Equal(t, "OFF", state.State)
	require.Zero(t, state.LatestVaultRevision)
	require.Nil(t, state.MigrationVerifiedAt, "backfill alone must not claim that the SQLite import was verified")

	var eventCount int64
	require.NoError(t, db.Model(&model.SyncEvent{}).Count(&eventCount).Error)
	require.Zero(t, eventCount, "backfill must not invent historical events")
}
