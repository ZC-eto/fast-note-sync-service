package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSafeSyncServiceTest(t *testing.T, databaseType string, verified bool) (*safeSyncService, *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "safe-sync-service.sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath, EnableWriteQueue: util.Ptr(false)}
	d := dao.New(mainDB, ctx,
		dao.WithConfig(cfg),
		dao.WithUserDatabaseConfig(cfg),
		dao.WithLogger(zap.NewNop()),
	)
	uow := dao.NewSafeSyncUnitOfWork(d)
	require.NoError(t, uow.Migrate(ctx, 1))
	userDB := d.ResolveDB(uow.GetKey(1))
	if verified {
		now := time.Now().UTC()
		require.NoError(t, userDB.Create(&model.SafeSyncMigrationState{
			ID: 1, Status: "VERIFIED", ReportDigest: "verified-report", VerifiedAt: &now,
		}).Error)
	}
	service := NewSafeSyncService(uow, databaseType, []byte("safe-sync-test-cursor-key")).(*safeSyncService)
	t.Cleanup(func() {
		if sqlDB, closeErr := userDB.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		if sqlDB, closeErr := mainDB.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return service, userDB
}

func TestSafeSyncService_StatusRequiresPostgresAndVerifiedImport(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name         string
		databaseType string
		verified     bool
	}{
		{name: "sqlite", databaseType: "sqlite", verified: true},
		{name: "postgres-unverified", databaseType: "postgres", verified: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := setupSafeSyncServiceTest(t, test.databaseType, test.verified)
			status, err := service.Status(ctx, 1, 7)
			require.Error(t, err)
			require.False(t, status.Capability)
			require.Equal(t, domain.SafeSyncErrorUnsupported, safeSyncErrorCode(err))
		})
	}

	service, _ := setupSafeSyncServiceTest(t, "postgres", true)
	status, err := service.Status(ctx, 1, 7)
	require.NoError(t, err)
	require.True(t, status.Capability)
	require.True(t, status.MigrationVerified)
	require.Equal(t, string(domain.VaultSyncStateOff), status.State)
	require.Equal(t, int64(1), status.UID)
	require.Equal(t, int64(7), status.VaultID)
}

func TestSafeSyncService_DeviceRolesAndPublisherLease(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	publisher, err := service.RegisterDeviceRole(ctx, 1, 8, "publisher-a", domain.DeviceSyncRoleLocalPublisher)
	require.NoError(t, err)
	require.True(t, publisher.Writable)
	require.Equal(t, "publisher-a", publisher.PublisherDeviceID)
	require.Equal(t, now.Add(devicePublisherLeaseTTL).UnixMilli(), publisher.PublisherLeaseExpiresAt)

	reader, err := service.RegisterDeviceRole(ctx, 1, 8, "reader-a", domain.DeviceSyncRoleRemoteMirror)
	require.NoError(t, err)
	require.False(t, reader.Writable)
	require.Equal(t, "publisher-a", reader.PublisherDeviceID)

	bidirectional, err := service.RegisterDeviceRole(ctx, 1, 8, "device-b", domain.DeviceSyncRoleBidirectional)
	require.NoError(t, err)
	require.False(t, bidirectional.Writable)

	_, err = service.RegisterDeviceRole(ctx, 1, 8, "publisher-b", domain.DeviceSyncRoleLocalPublisher)
	require.Equal(t, domain.SafeSyncErrorDeviceRoleConflict, safeSyncErrorCode(err))

	now = now.Add(devicePublisherLeaseTTL + time.Second)
	second, err := service.RegisterDeviceRole(ctx, 1, 8, "publisher-b", domain.DeviceSyncRoleLocalPublisher)
	require.NoError(t, err)
	require.True(t, second.Writable)
	require.Equal(t, "publisher-b", second.PublisherDeviceID)

	var roles []model.DeviceSyncRole
	require.NoError(t, db.Where("vault_id = ?", 8).Order("device_id").Find(&roles).Error)
	require.Len(t, roles, 4)
}

func TestSafeSyncService_ReleasesOwnPublisherLeaseWhenRoleChanges(t *testing.T) {
	ctx := context.Background()
	service, _ := setupSafeSyncServiceTest(t, "postgres", true)
	service.now = func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }

	_, err := service.RegisterDeviceRole(ctx, 1, 18, "publisher-a", domain.DeviceSyncRoleLocalPublisher)
	require.NoError(t, err)
	released, err := service.RegisterDeviceRole(ctx, 1, 18, "publisher-a", domain.DeviceSyncRoleBidirectional)
	require.NoError(t, err)
	require.True(t, released.Writable)
	require.Empty(t, released.PublisherDeviceID)
	require.Zero(t, released.PublisherLeaseExpiresAt)

	second, err := service.RegisterDeviceRole(ctx, 1, 18, "publisher-b", domain.DeviceSyncRoleLocalPublisher)
	require.NoError(t, err)
	require.True(t, second.Writable)
	require.Equal(t, "publisher-b", second.PublisherDeviceID)
}

func TestSafeSyncService_BootstrapPagesAndCommitsStrictWithoutDowngrade(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	resources := []model.SyncResourceMetadata{
		{ResourceID: "resource-c", VaultID: 9, ResourceType: "FOLDER", LegacyID: 3, ResourceRevision: 1, CurrentPath: "docs", CurrentPathHash: "path-c", State: "LIVE"},
		{ResourceID: "resource-a", VaultID: 9, ResourceType: "NOTE", LegacyID: 1, ResourceRevision: 1, CurrentPath: "a.md", CurrentPathHash: "path-a", ContentHash: "hash-a", State: "LIVE", Size: 10},
		{ResourceID: "resource-b", VaultID: 9, ResourceType: "FILE", LegacyID: 2, ResourceRevision: 1, CurrentPath: "b.png", CurrentPathHash: "path-b", ContentHash: "hash-b", State: "DELETED", Size: 20},
	}
	require.NoError(t, db.Create(&resources).Error)

	started, err := service.BootstrapStart(ctx, 1, 9, "device-a")
	require.NoError(t, err)
	require.Equal(t, string(domain.VaultSyncStateBootstrapping), started.State)
	require.NotEmpty(t, started.SessionID)
	require.NotEmpty(t, started.ManifestHash)
	require.NotEmpty(t, started.Cursor)
	require.Equal(t, int64(3), started.ResourceCount)

	_, err = service.BootstrapStart(ctx, 1, 9, "device-b")
	require.Equal(t, domain.SafeSyncErrorBootstrapInProgress, safeSyncErrorCode(err))

	firstPage, err := service.BootstrapPage(ctx, 1, 9, started.SessionID, started.Cursor, 2)
	require.NoError(t, err)
	require.Len(t, firstPage.Items, 2)
	require.Equal(t, "resource-a", firstPage.Items[0].ResourceID)
	require.NotEmpty(t, firstPage.NextCursor)
	secondPage, err := service.BootstrapPage(ctx, 1, 9, started.SessionID, firstPage.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, secondPage.Items, 1)
	require.Equal(t, "resource-c", secondPage.Items[0].ResourceID)
	require.Empty(t, secondPage.NextCursor)

	_, err = service.BootstrapCommit(ctx, 1, 9, started.SessionID, "wrong-hash", started.SnapshotVaultRevision)
	require.Equal(t, domain.SafeSyncErrorBootstrapStateConflict, safeSyncErrorCode(err))
	committed, err := service.BootstrapCommit(ctx, 1, 9, started.SessionID, started.ManifestHash, started.SnapshotVaultRevision)
	require.NoError(t, err)
	require.Equal(t, string(domain.VaultSyncStateStrict), committed.State)

	_, err = service.BootstrapCancel(ctx, 1, 9, started.SessionID)
	require.Equal(t, domain.SafeSyncErrorStrictRequired, safeSyncErrorCode(err))
	status, err := service.Status(ctx, 1, 9)
	require.NoError(t, err)
	require.Equal(t, string(domain.VaultSyncStateStrict), status.State)
}

func TestSafeSyncService_EventsRequireAvailableCursor(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.VaultSyncState{
		VaultID: 12, State: "STRICT", LatestVaultRevision: 7, MigrationVerifiedAt: &now,
	}).Error)
	events := []model.SyncEvent{
		{VaultID: 12, VaultRevision: 5, ResourceID: "r5", ResourceRevision: 1, ResourceType: "NOTE", Action: "CREATE", State: "LIVE", TransactionID: "t5", OperationID: "o5"},
		{VaultID: 12, VaultRevision: 6, ResourceID: "r6", ResourceRevision: 1, ResourceType: "NOTE", Action: "MODIFY", State: "LIVE", TransactionID: "t6", OperationID: "o6"},
		{VaultID: 12, VaultRevision: 7, ResourceID: "r7", ResourceRevision: 1, ResourceType: "FILE", Action: "DELETE", State: "DELETED", TransactionID: "t7", OperationID: "o7"},
	}
	require.NoError(t, db.Create(&events).Error)

	_, err := service.Events(ctx, 1, 12, 0, 2)
	require.Equal(t, domain.SafeSyncErrorRebootstrapRequired, safeSyncErrorCode(err))
	page, err := service.Events(ctx, 1, 12, 4, 2)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, int64(6), page.NextRevision)
	require.True(t, page.HasMore)
	last, err := service.Events(ctx, 1, 12, page.NextRevision, 2)
	require.NoError(t, err)
	require.Len(t, last.Events, 1)
	require.False(t, last.HasMore)
}
