package dao

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestFolderRepository_GetByPathHashSelectsLatestLiveProjection(t *testing.T) {
	daoInst, cleanup := setupCountByFIDsTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)
	const vaultID = int64(7)
	repo := NewFolderRepository(daoInst).(*folderRepository)

	_, err := repo.GetByFID(ctx, 0, vaultID, uid)
	require.NoError(t, err)
	db := daoInst.ResolveDB(repo.GetKey(uid))
	path := "英语/练习"
	pathHash := util.EncodeHash32(path)
	require.NoError(t, db.Create(&model.Folder{
		ID: 10, VaultID: vaultID, Action: "delete", Path: path, PathHash: pathHash,
	}).Error)
	require.NoError(t, db.Create(&model.Folder{
		ID: 11, VaultID: vaultID, Action: "create", Path: path, PathHash: pathHash,
	}).Error)
	require.NoError(t, db.Create(&model.Folder{
		ID: 12, VaultID: vaultID, Action: "create", Path: path, PathHash: pathHash,
	}).Error)

	folder, err := repo.GetByPathHash(ctx, pathHash, vaultID, uid)
	require.NoError(t, err)
	require.Equal(t, int64(12), folder.ID)
	require.Equal(t, domain.FolderActionCreate, folder.Action)
}

func TestFolderRepository_StrictReadsOnlyLiveSafeProjection(t *testing.T) {
	daoInst, cleanup := setupCountByFIDsTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)
	const vaultID = int64(8)
	repo := NewFolderRepository(daoInst).(*folderRepository)
	_, err := repo.GetByFID(ctx, 0, vaultID, uid)
	require.NoError(t, err)
	db := daoInst.ResolveDB(repo.GetKey(uid))
	require.NoError(t, db.AutoMigrate(&model.VaultSyncState{}, &model.SyncResourceMetadata{}))
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: vaultID, State: "STRICT"}).Error)

	english := model.Folder{ID: 20, VaultID: vaultID, Action: "create", Path: "英语", PathHash: util.EncodeHash32("英语"), UpdatedTimestamp: 1}
	exercise := model.Folder{ID: 21, VaultID: vaultID, Action: "create", Path: "英语/练习", PathHash: util.EncodeHash32("英语/练习"), FID: english.ID, UpdatedTimestamp: 1}
	staleRoot := model.Folder{ID: 22, VaultID: vaultID, Action: "create", Path: "练习", PathHash: util.EncodeHash32("练习"), UpdatedTimestamp: 1}
	require.NoError(t, db.Create(&english).Error)
	require.NoError(t, db.Create(&exercise).Error)
	require.NoError(t, db.Create(&staleRoot).Error)
	require.NoError(t, db.Create(&[]model.SyncResourceMetadata{
		{ResourceID: "folder-english", VaultID: vaultID, ResourceType: "FOLDER", LegacyID: english.ID, ResourceRevision: 1, CurrentPath: english.Path, CurrentPathHash: english.PathHash, State: "LIVE"},
		{ResourceID: "folder-exercise", VaultID: vaultID, ResourceType: "FOLDER", LegacyID: exercise.ID, ResourceRevision: 1, CurrentPath: exercise.Path, CurrentPathHash: exercise.PathHash, State: "LIVE"},
	}).Error)

	rootFolders, err := repo.GetByFID(ctx, 0, vaultID, uid)
	require.NoError(t, err)
	require.Equal(t, []string{"英语"}, folderPaths(rootFolders))
	childFolders, err := repo.GetByFID(ctx, english.ID, vaultID, uid)
	require.NoError(t, err)
	require.Equal(t, []string{"英语/练习"}, folderPaths(childFolders))
	selected, err := repo.GetByPathHash(ctx, exercise.PathHash, vaultID, uid)
	require.NoError(t, err)
	require.Equal(t, exercise.ID, selected.ID)

	all, err := repo.ListByUpdatedTimestamp(ctx, 0, vaultID, uid)
	require.NoError(t, err)
	require.Equal(t, []string{"英语", "英语/练习"}, folderPaths(all))
}

func folderPaths(folders []*domain.Folder) []string {
	paths := make([]string, 0, len(folders))
	for _, folder := range folders {
		paths = append(paths, folder.Path)
	}
	return paths
}
