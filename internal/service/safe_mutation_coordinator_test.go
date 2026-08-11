package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

func configureSafeContentStorage(t *testing.T, coordinator *SafeMutationCoordinator) string {
	t.Helper()
	volumeRoot := t.TempDir()
	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	coordinator.stager = stager
	coordinator.filePath = func(_, fileID int64) string {
		return filepath.Join(volumeRoot, "files", strconv.FormatInt(fileID, 10), "file.dat")
	}
	coordinator.notePath = func(_, noteID int64) string {
		return filepath.Join(volumeRoot, "notes", strconv.FormatInt(noteID, 10), "content.txt")
	}
	return volumeRoot
}

func TestStrictVaultWriteGuard_FailsClosedOnlyForEnabledVaults(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	guard := NewStrictVaultWriteGuard(service.uow, "postgres")
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 41, State: "OFF"}).Error)

	require.NoError(t, guard.CheckLegacyWrite(ctx, 1, 41))
	require.NoError(t, db.Model(&model.VaultSyncState{}).Where("vault_id = ?", 41).Update("state", "BOOTSTRAPPING").Error)
	require.ErrorIs(t, guard.CheckLegacyWrite(ctx, 1, 41), code.ErrorSafeSyncBootstrapProgress)
	require.NoError(t, db.Model(&model.VaultSyncState{}).Where("vault_id = ?", 41).Update("state", "STRICT").Error)
	require.ErrorIs(t, guard.CheckLegacyWrite(ctx, 1, 41), code.ErrorSafeSyncStrictRequired)

	unsupported := NewStrictVaultWriteGuard(service.uow, "sqlite")
	require.NoError(t, unsupported.CheckLegacyWrite(ctx, 1, 41))
}

func TestSafeMutationCoordinator_NoteIdempotencyAndRevisionConflict(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 42, State: "STRICT"}).Error)
	coordinator := NewSafeMutationCoordinator(service.uow)
	configureSafeContentStorage(t, coordinator)

	create := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-create", ExpectedPathState: "ABSENT", Action: "CREATE",
		Path: "notes/a.md", PathHash: util.EncodeHash32("notes/a.md"), Content: "one",
		ContentHash: util.EncodeHash32("one"), Size: 3, Ctime: 100, Mtime: 100,
	}
	created, err := coordinator.Mutate(ctx, 1, 42, domain.SyncResourceTypeNote, create)
	require.NoError(t, err)
	require.NotEmpty(t, created.ResourceID)
	require.Equal(t, int64(1), created.ResourceRevision)
	require.Equal(t, int64(1), created.VaultRevision)

	replayRequest := *create
	replayRequest.Context = "a-new-websocket-request-context"
	replayed, err := coordinator.Mutate(ctx, 1, 42, domain.SyncResourceTypeNote, &replayRequest)
	require.NoError(t, err)
	require.Equal(t, created.ResourceID, replayed.ResourceID)
	require.Equal(t, created.VaultRevision, replayed.VaultRevision)
	require.True(t, replayed.Replayed)
	var eventCount int64
	require.NoError(t, db.Model(&model.SyncEvent{}).Count(&eventCount).Error)
	require.Equal(t, int64(1), eventCount)

	reused := *create
	reused.Content = "different"
	reused.ContentHash = util.EncodeHash32(reused.Content)
	reused.Size = int64(len([]byte(reused.Content)))
	_, err = coordinator.Mutate(ctx, 1, 42, domain.SyncResourceTypeNote, &reused)
	require.Equal(t, domain.SafeSyncErrorOperationIDReused, safeSyncErrorCode(err))

	modify := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-modify", ResourceID: created.ResourceID,
		BaseRevision: 1, BaseHash: create.ContentHash, ExpectedPathState: "PRESENT", Action: "MODIFY",
		Path: create.Path, PathHash: create.PathHash, Content: "two", ContentHash: util.EncodeHash32("two"),
		Size: 3, Ctime: 100, Mtime: 200,
	}
	modified, err := coordinator.Mutate(ctx, 1, 42, domain.SyncResourceTypeNote, modify)
	require.NoError(t, err)
	require.Equal(t, int64(2), modified.ResourceRevision)
	require.Equal(t, int64(2), modified.VaultRevision)

	staleDelete := &dto.SafeMutationRequest{
		DeviceID: "device-b", OperationID: "op-delete-stale", ResourceID: created.ResourceID,
		BaseRevision: 1, BaseHash: create.ContentHash, ExpectedPathState: "PRESENT", Action: "DELETE",
		Path: create.Path, PathHash: create.PathHash,
	}
	_, err = coordinator.Mutate(ctx, 1, 42, domain.SyncResourceTypeNote, staleDelete)
	require.Equal(t, domain.SafeSyncErrorRevisionConflict, safeSyncErrorCode(err))

	var note model.Note
	require.NoError(t, db.Where("vault_id = ? AND path = ?", 42, create.Path).Take(&note).Error)
	require.Empty(t, note.Content)
	content, err := os.ReadFile(coordinator.notePath(1, note.ID))
	require.NoError(t, err)
	require.Equal(t, []byte("two"), content)
	require.NotEqual(t, "delete", note.Action)
	require.NoError(t, db.Model(&model.SyncEvent{}).Count(&eventCount).Error)
	require.Equal(t, int64(2), eventCount)
}

func TestSafeMutationFingerprintIgnoresTransportContext(t *testing.T) {
	request := &dto.SafeMutationRequest{
		Context: "first", DeviceID: "device-a", OperationID: "op-a", ExpectedPathState: "ABSENT",
		Action: "CREATE", Path: "notes/a.md", PathHash: util.EncodeHash32("notes/a.md"),
		Content: "one", ContentHash: util.EncodeHash32("one"), Size: 3,
	}
	first, err := safeMutationFingerprint(42, domain.SyncResourceTypeNote, request)
	require.NoError(t, err)
	request.Context = "second"
	second, err := safeMutationFingerprint(42, domain.SyncResourceTypeNote, request)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestReplayRejectedOperationPreservesOriginalErrorAcrossLegacyFingerprint(t *testing.T) {
	operation := &model.SyncOperation{
		State: "REJECTED", RequestFingerprint: "legacy-context-fingerprint",
		ErrorCode: string(domain.SafeSyncErrorPathStateConflict), ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err := replaySafeSyncOperation(operation, "normalized-fingerprint", time.Now())
	require.Equal(t, domain.SafeSyncErrorPathStateConflict, safeSyncErrorCode(err))
}

func TestSafeMutationCoordinator_RejectsInvalidNoteContentMetadata(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 49, State: "STRICT"}).Error)
	coordinator := NewSafeMutationCoordinator(service.uow)
	configureSafeContentStorage(t, coordinator)

	request := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "invalid-note", ExpectedPathState: "ABSENT", Action: "CREATE",
		Path: "notes/invalid.md", PathHash: util.EncodeHash32("notes/invalid.md"), Content: "中文",
		ContentHash: util.EncodeHash32("other"), Size: int64(len([]byte("中文"))), Ctime: 100, Mtime: 100,
	}
	_, err := coordinator.Mutate(ctx, 1, 49, domain.SyncResourceTypeNote, request)
	require.Equal(t, domain.SafeSyncErrorPathStateConflict, safeSyncErrorCode(err))

	request.OperationID = "invalid-note-size"
	request.ContentHash = util.EncodeHash32(request.Content)
	request.Size--
	_, err = coordinator.Mutate(ctx, 1, 49, domain.SyncResourceTypeNote, request)
	require.Equal(t, domain.SafeSyncErrorPathStateConflict, safeSyncErrorCode(err))

	request.OperationID = "valid-unicode-note"
	request.Size = int64(len([]byte(request.Content)))
	created, err := coordinator.Mutate(ctx, 1, 49, domain.SyncResourceTypeNote, request)
	require.NoError(t, err)
	var resource model.SyncResourceMetadata
	require.NoError(t, db.Where("resource_id = ?", created.ResourceID).Take(&resource).Error)
	content, err := os.ReadFile(coordinator.notePath(1, resource.LegacyID))
	require.NoError(t, err)
	require.Equal(t, []byte(request.Content), content)
}

func TestSafeMutationCoordinator_EnforcesDeviceRoles(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 48, State: "STRICT"}).Error)
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	require.NoError(t, db.Create(&[]model.DeviceSyncRole{
		{VaultID: 48, DeviceID: "publisher-a", Role: string(domain.DeviceSyncRoleLocalPublisher), LeaseExpiresAt: &expires, LastSeenAt: now},
		{VaultID: 48, DeviceID: "mirror-a", Role: string(domain.DeviceSyncRoleRemoteMirror), LastSeenAt: now},
	}).Error)
	coordinator := NewSafeMutationCoordinator(service.uow)
	configureSafeContentStorage(t, coordinator)
	coordinator.now = func() time.Time { return now }

	request := func(deviceID, operationID string) *dto.SafeMutationRequest {
		return &dto.SafeMutationRequest{
			DeviceID: deviceID, OperationID: operationID, ExpectedPathState: "ABSENT", Action: "CREATE",
			Path: operationID + ".md", PathHash: util.EncodeHash32(operationID + ".md"), Content: "one",
			ContentHash: util.EncodeHash32("one"), Size: 3, Ctime: 100, Mtime: 100,
		}
	}

	_, err := coordinator.Mutate(ctx, 1, 48, domain.SyncResourceTypeNote, request("mirror-a", "mirror-write"))
	require.Equal(t, domain.SafeSyncErrorDeviceReadOnly, safeSyncErrorCode(err))
	_, err = coordinator.Mutate(ctx, 1, 48, domain.SyncResourceTypeNote, request("device-b", "blocked-write"))
	require.Equal(t, domain.SafeSyncErrorDeviceRoleConflict, safeSyncErrorCode(err))
	_, err = coordinator.Mutate(ctx, 1, 48, domain.SyncResourceTypeNote, request("publisher-a", "publisher-write"))
	require.NoError(t, err)
}

func TestSafeMutationCoordinator_UsesRevisionInsteadOfClientClock(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 47, State: "STRICT"}).Error)
	coordinator := NewSafeMutationCoordinator(service.uow)
	configureSafeContentStorage(t, coordinator)

	create := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-clock-create", ExpectedPathState: "ABSENT", Action: "CREATE",
		Path: "notes/clock.md", PathHash: util.EncodeHash32("notes/clock.md"), Content: "initial",
		ContentHash: util.EncodeHash32("initial"), Size: 7, Ctime: 9_999_999_999_999, Mtime: 9_999_999_999_999,
	}
	created, err := coordinator.Mutate(ctx, 1, 47, domain.SyncResourceTypeNote, create)
	require.NoError(t, err)

	first := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-clock-first", ResourceID: created.ResourceID,
		BaseRevision: 1, BaseHash: create.ContentHash, ExpectedPathState: "PRESENT", Action: "MODIFY",
		Path: create.Path, PathHash: create.PathHash, Content: "first", ContentHash: util.EncodeHash32("first"),
		Size: 5, Ctime: create.Ctime, Mtime: create.Mtime,
	}
	firstResult, err := coordinator.Mutate(ctx, 1, 47, domain.SyncResourceTypeNote, first)
	require.NoError(t, err)
	require.Equal(t, int64(2), firstResult.ResourceRevision)

	staleFuture := *first
	staleFuture.DeviceID = "device-b"
	staleFuture.OperationID = "op-clock-stale-future"
	staleFuture.Content = "stale-future"
	staleFuture.ContentHash = util.EncodeHash32(staleFuture.Content)
	staleFuture.Size = int64(len([]byte(staleFuture.Content)))
	staleFuture.Mtime = first.Mtime + 1_000_000_000
	_, err = coordinator.Mutate(ctx, 1, 47, domain.SyncResourceTypeNote, &staleFuture)
	require.Equal(t, domain.SafeSyncErrorRevisionConflict, safeSyncErrorCode(err))

	currentOldClock := *first
	currentOldClock.DeviceID = "device-c"
	currentOldClock.OperationID = "op-clock-current-old"
	currentOldClock.BaseRevision = firstResult.ResourceRevision
	currentOldClock.BaseHash = first.ContentHash
	currentOldClock.Content = "current-old-clock"
	currentOldClock.ContentHash = util.EncodeHash32(currentOldClock.Content)
	currentOldClock.Size = int64(len([]byte(currentOldClock.Content)))
	currentOldClock.Mtime = 1
	currentResult, err := coordinator.Mutate(ctx, 1, 47, domain.SyncResourceTypeNote, &currentOldClock)
	require.NoError(t, err)
	require.Equal(t, int64(3), currentResult.ResourceRevision)

	var note model.Note
	require.NoError(t, db.Where("vault_id = ? AND path = ?", 47, create.Path).Take(&note).Error)
	require.Empty(t, note.Content)
	content, err := os.ReadFile(coordinator.notePath(1, note.ID))
	require.NoError(t, err)
	require.Equal(t, []byte(currentOldClock.Content), content)
}

func TestSafeMutationCoordinator_RecursiveFolderRenameIsAtomic(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}, &model.File{}, &model.Folder{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 43, State: "STRICT"}).Error)
	now := time.Now().UnixMilli()
	folder := model.Folder{ID: 1, VaultID: 43, Action: "create", Path: "old", PathHash: util.EncodeHash32("old"), UpdatedTimestamp: now}
	note := model.Note{ID: 2, VaultID: 43, Action: "create", Path: "old/a.md", PathHash: util.EncodeHash32("old/a.md"), Content: "a", ContentHash: util.EncodeHash32("a"), Size: 1, UpdatedTimestamp: now}
	file := model.File{ID: 3, VaultID: 43, Action: "create", Path: "old/a.bin", PathHash: util.EncodeHash32("old/a.bin"), ContentHash: "file-hash", Size: 4, UpdatedTimestamp: now}
	require.NoError(t, db.Create(&folder).Error)
	require.NoError(t, db.Create(&note).Error)
	require.NoError(t, db.Create(&file).Error)
	resources := []model.SyncResourceMetadata{
		{ResourceID: "folder-root", VaultID: 43, ResourceType: "FOLDER", LegacyID: 1, ResourceRevision: 1, CurrentPath: folder.Path, CurrentPathHash: folder.PathHash, State: "LIVE"},
		{ResourceID: "note-child", VaultID: 43, ResourceType: "NOTE", LegacyID: 2, ResourceRevision: 1, CurrentPath: note.Path, CurrentPathHash: note.PathHash, ContentHash: note.ContentHash, State: "LIVE", Size: 1},
		{ResourceID: "file-child", VaultID: 43, ResourceType: "FILE", LegacyID: 3, ResourceRevision: 1, CurrentPath: file.Path, CurrentPathHash: file.PathHash, ContentHash: file.ContentHash, State: "LIVE", Size: 4},
	}
	require.NoError(t, db.Create(&resources).Error)

	coordinator := NewSafeMutationCoordinator(service.uow)
	rename := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-folder-rename", ResourceID: "folder-root",
		BaseRevision: 1, ExpectedPathState: "PRESENT", Action: "RENAME",
		PreviousPath: "old", PreviousPathHash: folder.PathHash, Path: "new", PathHash: util.EncodeHash32("new"),
	}
	result, err := coordinator.Mutate(ctx, 1, 43, domain.SyncResourceTypeFolder, rename)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.ResourceRevision)
	require.Equal(t, int64(3), result.VaultRevision)

	var renamed []model.SyncResourceMetadata
	require.NoError(t, db.Where("vault_id = ?", 43).Order("resource_id").Find(&renamed).Error)
	require.Len(t, renamed, 3)
	for _, resource := range renamed {
		require.Equal(t, int64(2), resource.ResourceRevision)
		require.NotContains(t, resource.CurrentPath, "old")
		require.True(t, resource.CurrentPath == "new" || len(resource.CurrentPath) > len("new/"))
	}
	var events []model.SyncEvent
	require.NoError(t, db.Where("vault_id = ?", 43).Order("vault_revision").Find(&events).Error)
	require.Len(t, events, 3)
	require.Equal(t, []int64{1, 2, 3}, []int64{events[0].VaultRevision, events[1].VaultRevision, events[2].VaultRevision})
	require.Equal(t, events[0].TransactionID, events[1].TransactionID)
	require.Equal(t, events[1].TransactionID, events[2].TransactionID)
	var tombstoneCount int64
	require.NoError(t, db.Model(&model.SyncPathTombstone{}).Where("vault_id = ?", 43).Count(&tombstoneCount).Error)
	require.Equal(t, int64(3), tombstoneCount)

	conflict := model.SyncResourceMetadata{ResourceID: "target-conflict", VaultID: 43, ResourceType: "NOTE", LegacyID: 99, ResourceRevision: 1, CurrentPath: "blocked/a.md", CurrentPathHash: util.EncodeHash32("blocked/a.md"), State: "LIVE"}
	require.NoError(t, db.Create(&conflict).Error)
	blocked := *rename
	blocked.OperationID = "op-folder-blocked"
	blocked.BaseRevision = 2
	blocked.PreviousPath = "new"
	blocked.PreviousPathHash = util.EncodeHash32("new")
	blocked.Path = "blocked"
	blocked.PathHash = util.EncodeHash32("blocked")
	_, err = coordinator.Mutate(ctx, 1, 43, domain.SyncResourceTypeFolder, &blocked)
	require.Equal(t, domain.SafeSyncErrorPathStateConflict, safeSyncErrorCode(err))

	var root model.SyncResourceMetadata
	require.NoError(t, db.Where("resource_id = ?", "folder-root").Take(&root).Error)
	require.Equal(t, "new", root.CurrentPath)
	require.Equal(t, int64(2), root.ResourceRevision)

	deleteTree := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-folder-delete", ResourceID: "folder-root",
		BaseRevision: 2, ExpectedPathState: "PRESENT", Action: "DELETE",
		Path: "new", PathHash: util.EncodeHash32("new"),
	}
	deleted, err := coordinator.Mutate(ctx, 1, 43, domain.SyncResourceTypeFolder, deleteTree)
	require.NoError(t, err)
	require.Equal(t, int64(6), deleted.VaultRevision)

	var deleteEvents []model.SyncEvent
	require.NoError(t, db.Where("vault_id = ? AND action = ?", 43, "DELETE").Order("vault_revision").Find(&deleteEvents).Error)
	require.Len(t, deleteEvents, 3)
	require.NotEqual(t, "new", deleteEvents[0].Path)
	require.NotEqual(t, "new", deleteEvents[1].Path)
	require.Equal(t, "new", deleteEvents[2].Path, "folder delete must emit descendants before the root")
}

func TestSafeMutationCoordinator_FileCommitIsExplicitAndIdempotent(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.File{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 44, State: "STRICT"}).Error)

	volumeRoot := t.TempDir()
	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	coordinator := NewSafeMutationCoordinator(service.uow)
	coordinator.stager = stager
	coordinator.filePath = func(_, fileID int64) string {
		return filepath.Join(volumeRoot, "files", strconv.FormatInt(fileID, 10), "file.dat")
	}
	coordinator.notePath = func(_, noteID int64) string {
		return filepath.Join(volumeRoot, "notes", strconv.FormatInt(noteID, 10), "content.txt")
	}

	firstContent := []byte{0, 1, 2, 3}
	firstUpload := filepath.Join(t.TempDir(), "first.upload")
	require.NoError(t, os.WriteFile(firstUpload, firstContent, 0o600))
	create := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-file-create", ExpectedPathState: "ABSENT", Action: "CREATE",
		Path: "assets/a.bin", PathHash: util.EncodeHash32("assets/a.bin"), ContentHash: util.EncodeHash32Bytes(firstContent),
		Size: int64(len(firstContent)), Ctime: 100, Mtime: 100,
	}
	created, err := coordinator.CommitFile(ctx, 1, 44, create, firstUpload)
	require.NoError(t, err)
	require.Equal(t, int64(1), created.ResourceRevision)
	require.Equal(t, int64(1), created.VaultRevision)

	replayed, err := coordinator.CommitFile(ctx, 1, 44, create, firstUpload)
	require.NoError(t, err)
	require.Equal(t, created.ResourceID, replayed.ResourceID)
	require.Equal(t, created.VaultRevision, replayed.VaultRevision)
	require.True(t, replayed.Replayed)

	secondContent := []byte{4, 5, 6}
	secondUpload := filepath.Join(t.TempDir(), "second.upload")
	require.NoError(t, os.WriteFile(secondUpload, secondContent, 0o600))
	modify := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-file-modify", ResourceID: created.ResourceID,
		BaseRevision: 1, BaseHash: create.ContentHash, ExpectedPathState: "PRESENT", Action: "MODIFY",
		Path: create.Path, PathHash: create.PathHash, ContentHash: util.EncodeHash32Bytes(secondContent),
		Size: int64(len(secondContent)), Ctime: 100, Mtime: 200,
	}
	modified, err := coordinator.CommitFile(ctx, 1, 44, modify, secondUpload)
	require.NoError(t, err)
	require.Equal(t, int64(2), modified.ResourceRevision)
	require.Equal(t, int64(2), modified.VaultRevision)

	var resource model.SyncResourceMetadata
	require.NoError(t, db.Where("resource_id = ?", created.ResourceID).Take(&resource).Error)
	content, err := os.ReadFile(coordinator.filePath(1, resource.LegacyID))
	require.NoError(t, err)
	require.Equal(t, secondContent, content)
	var eventCount int64
	require.NoError(t, db.Model(&model.SyncEvent{}).Where("vault_id = ?", 44).Count(&eventCount).Error)
	require.Equal(t, int64(2), eventCount)
	var preparedCount int64
	require.NoError(t, db.Model(&model.SyncOperation{}).Where("state = ?", "PREPARED").Count(&preparedCount).Error)
	require.Zero(t, preparedCount)
}

func TestSafeMutationCoordinator_RecoversAppliedPreparedFileExactlyOnce(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.File{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 45, State: "STRICT"}).Error)

	volumeRoot := t.TempDir()
	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	coordinator := NewSafeMutationCoordinator(service.uow)
	coordinator.stager = stager
	coordinator.filePath = func(_, fileID int64) string {
		return filepath.Join(volumeRoot, "files", strconv.FormatInt(fileID, 10), "file.dat")
	}
	coordinator.notePath = func(_, noteID int64) string {
		return filepath.Join(volumeRoot, "notes", strconv.FormatInt(noteID, 10), "content.txt")
	}

	oldContent := []byte("old")
	newContent := []byte("new")
	targetPath := coordinator.filePath(1, 1)
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, oldContent, 0o600))
	path := "assets/recover.bin"
	pathHash := util.EncodeHash32(path)
	oldHash := util.EncodeHash32Bytes(oldContent)
	newHash := util.EncodeHash32Bytes(newContent)
	require.NoError(t, db.Create(&model.File{
		ID: 1, VaultID: 45, Action: "create", Path: path, PathHash: pathHash,
		ContentHash: oldHash, Size: int64(len(oldContent)), UpdatedTimestamp: time.Now().UnixMilli(),
	}).Error)
	require.NoError(t, db.Create(&model.SyncResourceMetadata{
		ResourceID: "file-recover-applied", VaultID: 45, ResourceType: "FILE", LegacyID: 1,
		ResourceRevision: 1, CurrentPath: path, CurrentPathHash: pathHash,
		ContentHash: oldHash, State: "LIVE", Size: int64(len(oldContent)),
	}).Error)

	request := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-recover-applied", ResourceID: "file-recover-applied",
		BaseRevision: 1, BaseHash: oldHash, ExpectedPathState: "PRESENT", Action: "MODIFY",
		Path: path, PathHash: pathHash, ContentHash: newHash, Size: int64(len(newContent)),
		Ctime: 100, Mtime: 200,
	}
	fingerprint, err := safeMutationFingerprint(45, domain.SyncResourceTypeFile, request)
	require.NoError(t, err)
	staged, err := stager.Stage(ctx, request.OperationID, targetPath, newContent, newHash)
	require.NoError(t, err)
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	operation := model.SyncOperation{
		VaultID: 45, DeviceID: request.DeviceID, OperationID: request.OperationID,
		Action: "FILE:MODIFY", RequestFingerprint: fingerprint, State: "PREPARED",
		ResourceID: request.ResourceID, LegacyID: 1, RequestPayload: string(payload),
		StagedPath: staged.StagedPath, OldImagePath: staged.OldImagePath,
		TargetPath: staged.TargetPath, ExpectedHash: staged.ExpectedHash, TargetExisted: true,
	}
	require.NoError(t, db.Create(&operation).Error)
	require.NoError(t, stager.Apply(ctx, staged))

	require.NoError(t, coordinator.RecoverPrepared(ctx, 1))
	require.NoError(t, coordinator.RecoverPrepared(ctx, 1))

	var recovered model.SyncOperation
	require.NoError(t, db.Where("id = ?", operation.ID).Take(&recovered).Error)
	require.Equal(t, "COMMITTED", recovered.State)
	require.Equal(t, int64(2), recovered.ResourceRevision)
	require.Equal(t, int64(1), recovered.VaultRevision)
	var resource model.SyncResourceMetadata
	require.NoError(t, db.Where("resource_id = ?", request.ResourceID).Take(&resource).Error)
	require.Equal(t, int64(2), resource.ResourceRevision)
	require.Equal(t, newHash, resource.ContentHash)
	var eventCount int64
	require.NoError(t, db.Model(&model.SyncEvent{}).Where("vault_id = ?", 45).Count(&eventCount).Error)
	require.Equal(t, int64(1), eventCount)
	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, newContent, content)
	require.NoDirExists(t, filepath.Dir(staged.StagedPath))
	replayed, err := coordinator.CommitFile(ctx, 1, 45, request, filepath.Join(t.TempDir(), "missing.upload"))
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, int64(1), replayed.VaultRevision)
}

func TestSafeMutationCoordinator_RecoversAppliedPreparedNoteExactlyOnce(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.Note{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 50, State: "STRICT"}).Error)

	coordinator := NewSafeMutationCoordinator(service.uow)
	configureSafeContentStorage(t, coordinator)
	stager, err := coordinator.contentStager()
	require.NoError(t, err)
	oldContent := []byte("old note")
	newContent := []byte("new note 中文")
	targetPath := coordinator.notePath(1, 1)
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, oldContent, 0o600))
	path := "notes/recover.md"
	pathHash := util.EncodeHash32(path)
	oldHash := util.EncodeHash32Bytes(oldContent)
	newHash := util.EncodeHash32Bytes(newContent)
	require.NoError(t, db.Create(&model.Note{
		ID: 1, VaultID: 50, Action: "create", Path: path, PathHash: pathHash,
		ContentHash: oldHash, Size: int64(len(oldContent)), Version: 1, UpdatedTimestamp: time.Now().UnixMilli(),
	}).Error)
	require.NoError(t, db.Create(&model.SyncResourceMetadata{
		ResourceID: "note-recover-applied", VaultID: 50, ResourceType: "NOTE", LegacyID: 1,
		ResourceRevision: 1, CurrentPath: path, CurrentPathHash: pathHash,
		ContentHash: oldHash, State: "LIVE", Size: int64(len(oldContent)),
	}).Error)

	request := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-note-recover-applied", ResourceID: "note-recover-applied",
		BaseRevision: 1, BaseHash: oldHash, ExpectedPathState: "PRESENT", Action: "MODIFY",
		Path: path, PathHash: pathHash, Content: string(newContent), ContentHash: newHash,
		Size: int64(len(newContent)), Ctime: 100, Mtime: 200,
	}
	fingerprint, err := safeMutationFingerprint(50, domain.SyncResourceTypeNote, request)
	require.NoError(t, err)
	staged, err := stager.Stage(ctx, request.OperationID, targetPath, newContent, newHash)
	require.NoError(t, err)
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	operation := model.SyncOperation{
		VaultID: 50, DeviceID: request.DeviceID, OperationID: request.OperationID,
		Action: "NOTE:MODIFY", RequestFingerprint: fingerprint, State: "PREPARED",
		ResourceID: request.ResourceID, LegacyID: 1, RequestPayload: string(payload),
		StagedPath: staged.StagedPath, OldImagePath: staged.OldImagePath,
		TargetPath: staged.TargetPath, ExpectedHash: staged.ExpectedHash, TargetExisted: true,
	}
	require.NoError(t, db.Create(&operation).Error)
	require.NoError(t, stager.Apply(ctx, staged))

	require.NoError(t, coordinator.RecoverPrepared(ctx, 1))
	require.NoError(t, coordinator.RecoverPrepared(ctx, 1))

	var recovered model.SyncOperation
	require.NoError(t, db.Where("id = ?", operation.ID).Take(&recovered).Error)
	require.Equal(t, "COMMITTED", recovered.State)
	require.Empty(t, recovered.RequestPayload)
	require.Equal(t, int64(2), recovered.ResourceRevision)
	require.Equal(t, int64(1), recovered.VaultRevision)
	var note model.Note
	require.NoError(t, db.Where("id = ?", 1).Take(&note).Error)
	require.Empty(t, note.Content)
	require.Equal(t, newHash, note.ContentHash)
	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, newContent, content)
	var eventCount int64
	require.NoError(t, db.Model(&model.SyncEvent{}).Where("vault_id = ?", 50).Count(&eventCount).Error)
	require.Equal(t, int64(1), eventCount)
	require.NoDirExists(t, filepath.Dir(staged.StagedPath))
	replayed, err := coordinator.Mutate(ctx, 1, 50, domain.SyncResourceTypeNote, request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, int64(1), replayed.VaultRevision)
}

func TestSafeMutationCoordinator_RestoresIncompletePreparedBeforeNextMutationAndAllowsRetry(t *testing.T) {
	ctx := context.Background()
	service, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.AutoMigrate(&model.File{}, &model.Note{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 46, State: "STRICT"}).Error)

	volumeRoot := t.TempDir()
	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	coordinator := NewSafeMutationCoordinator(service.uow)
	coordinator.stager = stager
	coordinator.filePath = func(_, fileID int64) string {
		return filepath.Join(volumeRoot, "files", strconv.FormatInt(fileID, 10), "file.dat")
	}
	coordinator.notePath = func(_, noteID int64) string {
		return filepath.Join(volumeRoot, "notes", strconv.FormatInt(noteID, 10), "content.txt")
	}

	oldContent := []byte("old")
	newContent := []byte("new")
	targetPath := coordinator.filePath(1, 1)
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, oldContent, 0o600))
	path := "assets/retry.bin"
	pathHash := util.EncodeHash32(path)
	oldHash := util.EncodeHash32Bytes(oldContent)
	newHash := util.EncodeHash32Bytes(newContent)
	require.NoError(t, db.Create(&model.File{
		ID: 1, VaultID: 46, Action: "create", Path: path, PathHash: pathHash,
		ContentHash: oldHash, Size: int64(len(oldContent)), UpdatedTimestamp: time.Now().UnixMilli(),
	}).Error)
	require.NoError(t, db.Create(&model.SyncResourceMetadata{
		ResourceID: "file-recover-restored", VaultID: 46, ResourceType: "FILE", LegacyID: 1,
		ResourceRevision: 1, CurrentPath: path, CurrentPathHash: pathHash,
		ContentHash: oldHash, State: "LIVE", Size: int64(len(oldContent)),
	}).Error)

	request := &dto.SafeMutationRequest{
		DeviceID: "device-a", OperationID: "op-recover-restored", ResourceID: "file-recover-restored",
		BaseRevision: 1, BaseHash: oldHash, ExpectedPathState: "PRESENT", Action: "MODIFY",
		Path: path, PathHash: pathHash, ContentHash: newHash, Size: int64(len(newContent)),
		Ctime: 100, Mtime: 200,
	}
	fingerprint, err := safeMutationFingerprint(46, domain.SyncResourceTypeFile, request)
	require.NoError(t, err)
	staged, err := stager.Stage(ctx, request.OperationID, targetPath, newContent, newHash)
	require.NoError(t, err)
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	operation := model.SyncOperation{
		VaultID: 46, DeviceID: request.DeviceID, OperationID: request.OperationID,
		Action: "FILE:MODIFY", RequestFingerprint: fingerprint, State: "PREPARED",
		ResourceID: request.ResourceID, LegacyID: 1, RequestPayload: string(payload),
		StagedPath: staged.StagedPath, OldImagePath: staged.OldImagePath,
		TargetPath: staged.TargetPath, ExpectedHash: staged.ExpectedHash, TargetExisted: true,
	}
	require.NoError(t, db.Create(&operation).Error)
	require.NoError(t, os.Rename(targetPath, staged.OldImagePath))

	noteRequest := &dto.SafeMutationRequest{
		DeviceID: "device-b", OperationID: "op-after-recovery", ExpectedPathState: "ABSENT", Action: "CREATE",
		Path: "notes/after.md", PathHash: util.EncodeHash32("notes/after.md"), Content: "after",
		ContentHash: util.EncodeHash32("after"), Size: 5, Ctime: 100, Mtime: 100,
	}
	noteResult, err := coordinator.Mutate(ctx, 1, 46, domain.SyncResourceTypeNote, noteRequest)
	require.NoError(t, err)
	require.Equal(t, int64(1), noteResult.VaultRevision)
	restoredContent, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, oldContent, restoredContent)
	var preparedCount int64
	require.NoError(t, db.Model(&model.SyncOperation{}).Where("id = ?", operation.ID).Count(&preparedCount).Error)
	require.Zero(t, preparedCount)
	require.NoDirExists(t, filepath.Dir(staged.StagedPath))

	uploadPath := filepath.Join(t.TempDir(), "retry.upload")
	require.NoError(t, os.WriteFile(uploadPath, newContent, 0o600))
	retried, err := coordinator.CommitFile(ctx, 1, 46, request, uploadPath)
	require.NoError(t, err)
	require.Equal(t, int64(2), retried.ResourceRevision)
	require.Equal(t, int64(2), retried.VaultRevision)
	finalContent, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, newContent, finalContent)
}
