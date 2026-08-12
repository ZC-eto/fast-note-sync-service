package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
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

func TestSafeSyncService_BootstrapReconcilesLegacyContentBeforeBuildingManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
	require.NoError(t, db.Create(&model.Note{
		ID: 21, VaultID: 19, Action: "modify", Path: "changed.md", PathHash: "path-current",
		Content: "数据库中的旧内容", ContentHash: "stale-stored-hash", Size: 0,
	}).Error)
	noteContent := "content.txt 中的当前内容"
	noteFolder := filepath.Join("storage", "vault", "u_1", "note", "n_21")
	require.NoError(t, os.MkdirAll(noteFolder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noteFolder, "content.txt"), []byte(noteContent), 0o644))
	require.NoError(t, db.Create(&model.SyncResourceMetadata{
		ResourceID: "stale-note", VaultID: 19, ResourceType: "NOTE", LegacyID: 21,
		ResourceRevision: 1, CurrentPath: "changed.md", CurrentPathHash: "path-stale",
		ContentHash: "hash-stale", State: "LIVE", Size: 5,
	}).Error)

	started, err := service.BootstrapStart(ctx, 1, 19, "device-a")
	require.NoError(t, err)
	page, err := service.BootstrapPage(ctx, 1, 19, started.SessionID, started.Cursor, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "path-current", page.Items[0].PathHash)
	require.Equal(t, util.EncodeHash32(noteContent), page.Items[0].ContentHash)
	require.Equal(t, int64(len([]byte(noteContent))), page.Items[0].Size)
}

func TestSafeSyncService_BootstrapRejectsLegacyChangeBeforeCommit(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
	note := model.Note{
		ID: 22, VaultID: 20, Action: "modify", Path: "changed.md", PathHash: "path-current",
		Content: "before", ContentHash: "hash-before", Size: 6,
	}
	require.NoError(t, db.Create(&note).Error)
	noteFolder := filepath.Join("storage", "vault", "u_1", "note", "n_22")
	require.NoError(t, os.MkdirAll(noteFolder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noteFolder, "content.txt"), []byte("before"), 0o644))

	started, err := service.BootstrapStart(ctx, 1, 20, "device-a")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(noteFolder, "content.txt"), []byte("after"), 0o644))

	_, err = service.BootstrapCommit(ctx, 1, 20, started.SessionID, started.ManifestHash, started.SnapshotVaultRevision)
	require.Equal(t, domain.SafeSyncErrorBootstrapStateConflict, safeSyncErrorCode(err))

	var state model.VaultSyncState
	require.NoError(t, db.Where("vault_id = ?", 20).Take(&state).Error)
	require.Equal(t, string(domain.VaultSyncStateBootstrapping), state.State)
}

func TestSafeSyncService_BootstrapReconcilesAttachmentFromPhysicalContent(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
	require.NoError(t, db.Create(&model.File{
		ID: 23, VaultID: 21, Action: "modify", Path: "assets/current.bin", PathHash: "path-current",
		ContentHash: "stale-stored-hash", Size: 1,
	}).Error)
	legacyPath := filepath.Join("legacy", "attachment.bin")
	require.NoError(t, db.Create(&model.File{
		ID: 25, VaultID: 21, Action: "modify", Path: "assets/legacy.bin", PathHash: "path-legacy",
		ContentHash: "another-stale-hash", Size: 2, SavePath: legacyPath,
	}).Error)
	content := []byte{0, 1, 2, 3, 254, 255}
	fileFolder := filepath.Join("storage", "vault", "u_1", "file", "f_23")
	require.NoError(t, os.MkdirAll(fileFolder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fileFolder, "file.dat"), content, 0o644))
	legacyContent := []byte("legacy physical attachment")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o755))
	require.NoError(t, os.WriteFile(legacyPath, legacyContent, 0o644))

	started, err := service.BootstrapStart(ctx, 1, 21, "device-a")
	require.NoError(t, err)
	page, err := service.BootstrapPage(ctx, 1, 21, started.SessionID, started.Cursor, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	items := make(map[string]dto.SafeSyncManifestItem, len(page.Items))
	for _, item := range page.Items {
		items[item.Path] = item
	}
	require.Equal(t, util.EncodeHash32Bytes(content), items["assets/current.bin"].ContentHash)
	require.Equal(t, int64(len(content)), items["assets/current.bin"].Size)
	require.Equal(t, util.EncodeHash32Bytes(legacyContent), items["assets/legacy.bin"].ContentHash)
	require.Equal(t, int64(len(legacyContent)), items["assets/legacy.bin"].Size)
}

func TestSafeSyncService_BootstrapFailsClosedWhenLiveContentIsMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
	require.NoError(t, db.Create(&model.Note{
		ID: 24, VaultID: 22, Action: "modify", Path: "missing.md", PathHash: "path-missing",
	}).Error)

	_, err := service.BootstrapStart(ctx, 1, 22, "device-a")
	require.ErrorContains(t, err, "live note content is unavailable")

	var stateCount int64
	require.NoError(t, db.Model(&model.VaultSyncState{}).Where("vault_id = ?", 22).Count(&stateCount).Error)
	require.Zero(t, stateCount)
}

func TestSafeSyncService_StrictBootstrapReconcilesPhysicalContentOnce(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.VaultSyncState{
		VaultID: 23, State: string(domain.VaultSyncStateStrict), LatestVaultRevision: 40,
		MigrationVerifiedAt: &now,
	}).Error)

	noteContent := "strict note content"
	fileContent := []byte{0, 1, 2, 3, 254, 255}
	require.NoError(t, db.Create(&model.Note{
		ID: 31, VaultID: 23, Action: "modify", Path: "notes/current.md", PathHash: "note-path",
		Content: "stale note content", ContentHash: "stale-note-hash", Size: 1,
	}).Error)
	require.NoError(t, db.Create(&model.File{
		ID: 32, VaultID: 23, Action: "modify", Path: "assets/current.bin", PathHash: "file-path",
		ContentHash: "stale-file-hash", Size: 2,
	}).Error)
	noteFolder := filepath.Join("storage", "vault", "u_1", "note", "n_31")
	fileFolder := filepath.Join("storage", "vault", "u_1", "file", "f_32")
	require.NoError(t, os.MkdirAll(noteFolder, 0o755))
	require.NoError(t, os.MkdirAll(fileFolder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noteFolder, "content.txt"), []byte(noteContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fileFolder, "file.dat"), fileContent, 0o644))
	resources := []model.SyncResourceMetadata{
		{
			ResourceID: "strict-note", VaultID: 23, ResourceType: "NOTE", LegacyID: 31,
			ResourceRevision: 3, CurrentPath: "notes/current.md", CurrentPathHash: "note-path",
			ContentHash: "stale-note-hash", State: "LIVE", Size: 1,
		},
		{
			ResourceID: "strict-file", VaultID: 23, ResourceType: "FILE", LegacyID: 32,
			ResourceRevision: 7, CurrentPath: "assets/current.bin", CurrentPathHash: "file-path",
			ContentHash: "stale-file-hash", State: "LIVE", Size: 2,
		},
	}
	require.NoError(t, db.Create(&resources).Error)

	started, err := service.BootstrapStart(ctx, 1, 23, "device-a")
	require.NoError(t, err)
	require.Equal(t, int64(42), started.SnapshotVaultRevision)
	page, err := service.BootstrapPage(ctx, 1, 23, started.SessionID, started.Cursor, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	items := make(map[string]dto.SafeSyncManifestItem, len(page.Items))
	for _, item := range page.Items {
		items[item.ResourceID] = item
	}
	require.Equal(t, util.EncodeHash32(noteContent), items["strict-note"].ContentHash)
	require.Equal(t, int64(len([]byte(noteContent))), items["strict-note"].Size)
	require.Equal(t, int64(4), items["strict-note"].ResourceRevision)
	require.Equal(t, util.EncodeHash32Bytes(fileContent), items["strict-file"].ContentHash)
	require.Equal(t, int64(len(fileContent)), items["strict-file"].Size)
	require.Equal(t, int64(8), items["strict-file"].ResourceRevision)

	var events []model.SyncEvent
	require.NoError(t, db.Where("vault_id = ?", 23).Order("vault_revision").Find(&events).Error)
	require.Len(t, events, 2)
	require.Equal(t, []int64{41, 42}, []int64{events[0].VaultRevision, events[1].VaultRevision})
	require.Equal(t, []string{"MODIFY", "MODIFY"}, []string{events[0].Action, events[1].Action})

	_, err = service.BootstrapCancel(ctx, 1, 23, started.SessionID)
	require.NoError(t, err)
	second, err := service.BootstrapStart(ctx, 1, 23, "device-a")
	require.NoError(t, err)
	require.Equal(t, int64(42), second.SnapshotVaultRevision)
	var eventCount int64
	require.NoError(t, db.Model(&model.SyncEvent{}).Where("vault_id = ?", 23).Count(&eventCount).Error)
	require.Equal(t, int64(2), eventCount)
}

func TestSafeSyncService_StrictBootstrapCommitRollsBackPhysicalContentChanges(t *testing.T) {
	for _, test := range []struct {
		name         string
		resourceType string
		writeChanged func(t *testing.T, contentPath string)
	}{
		{
			name: "note", resourceType: "NOTE",
			writeChanged: func(t *testing.T, contentPath string) {
				t.Helper()
				require.NoError(t, os.WriteFile(contentPath, []byte("note after preview"), 0o644))
			},
		},
		{
			name: "file", resourceType: "FILE",
			writeChanged: func(t *testing.T, contentPath string) {
				t.Helper()
				require.NoError(t, os.WriteFile(contentPath, []byte("file after preview"), 0o644))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			ctx := context.Background()
			service, db := setupSafeSyncServiceTest(t, "postgres", true)
			require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
			now := time.Now().UTC()
			vaultID := int64(30)
			legacyID := int64(41)
			path := "current.md"
			pathHash := "current-path"
			content := []byte("content at preview")
			contentHash := util.EncodeHash32Bytes(content)
			contentFolder := filepath.Join("storage", "vault", "u_1", "note", "n_41")
			contentName := "content.txt"
			if test.resourceType == "FILE" {
				vaultID = 31
				legacyID = 42
				path = "current.bin"
				contentFolder = filepath.Join("storage", "vault", "u_1", "file", "f_42")
				contentName = "file.dat"
			}
			require.NoError(t, db.Create(&model.VaultSyncState{
				VaultID: vaultID, State: string(domain.VaultSyncStateStrict), LatestVaultRevision: 5,
				MigrationVerifiedAt: &now,
			}).Error)
			if test.resourceType == "NOTE" {
				require.NoError(t, db.Create(&model.Note{
					ID: legacyID, VaultID: vaultID, Action: "modify", Path: path, PathHash: pathHash,
					Content: string(content), ContentHash: contentHash, Size: int64(len(content)),
				}).Error)
			} else {
				require.NoError(t, db.Create(&model.File{
					ID: legacyID, VaultID: vaultID, Action: "modify", Path: path, PathHash: pathHash,
					ContentHash: contentHash, Size: int64(len(content)),
				}).Error)
			}
			require.NoError(t, os.MkdirAll(contentFolder, 0o755))
			contentPath := filepath.Join(contentFolder, contentName)
			require.NoError(t, os.WriteFile(contentPath, content, 0o644))
			require.NoError(t, db.Create(&model.SyncResourceMetadata{
				ResourceID: "strict-" + test.name, VaultID: vaultID, ResourceType: test.resourceType, LegacyID: legacyID,
				ResourceRevision: 2, CurrentPath: path, CurrentPathHash: pathHash,
				ContentHash: contentHash, State: "LIVE", Size: int64(len(content)),
			}).Error)

			started, err := service.BootstrapStart(ctx, 1, vaultID, "device-a")
			require.NoError(t, err)
			test.writeChanged(t, contentPath)
			_, err = service.BootstrapCommit(ctx, 1, vaultID, started.SessionID, started.ManifestHash, started.SnapshotVaultRevision)
			require.Equal(t, domain.SafeSyncErrorBootstrapStateConflict, safeSyncErrorCode(err))

			var state model.VaultSyncState
			require.NoError(t, db.Where("vault_id = ?", vaultID).Take(&state).Error)
			require.Equal(t, string(domain.VaultSyncStateBootstrapping), state.State)
			require.Equal(t, int64(5), state.LatestVaultRevision)
			var resource model.SyncResourceMetadata
			require.NoError(t, db.Where("resource_id = ?", "strict-"+test.name).Take(&resource).Error)
			require.Equal(t, contentHash, resource.ContentHash)
			require.Equal(t, int64(2), resource.ResourceRevision)
			var eventCount int64
			require.NoError(t, db.Model(&model.SyncEvent{}).Where("vault_id = ?", vaultID).Count(&eventCount).Error)
			require.Zero(t, eventCount)
			if test.resourceType == "NOTE" {
				var note model.Note
				require.NoError(t, db.Where("id = ?", legacyID).Take(&note).Error)
				require.Equal(t, contentHash, note.ContentHash)
			} else {
				var file model.File
				require.NoError(t, db.Where("id = ?", legacyID).Take(&file).Error)
				require.Equal(t, contentHash, file.ContentHash)
			}
		})
	}
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
