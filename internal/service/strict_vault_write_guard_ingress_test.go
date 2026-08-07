package service

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestStrictVaultWriteGuard_BlocksLegacyServiceIngresses(t *testing.T) {
	ctx := context.Background()
	safeSyncService, db := setupSafeSyncServiceTest(t, "postgres", true)
	require.NoError(t, db.Create(&model.VaultSyncState{VaultID: 55, State: "STRICT"}).Error)
	guard := NewStrictVaultWriteGuard(safeSyncService.uow, "postgres")

	vaultRepo := new(domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "strict-vault", int64(1)).Return(&domain.Vault{ID: 55, Name: "strict-vault"}, nil)
	vaultService := NewVaultService(vaultRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())

	noteService := NewNoteService(nil, nil, nil, nil, nil, vaultService, nil, nil, nil, nil, nil, guard).WithClient("obsidian", "desktop", "test")
	_, _, err := noteService.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{Vault: "strict-vault"}, false)
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, err = noteService.Delete(ctx, 1, &dto.NoteDeleteRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, err = noteService.Restore(ctx, 1, &dto.NoteRestoreRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, _, err = noteService.Rename(ctx, 1, &dto.NoteRenameRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)

	fileService := NewFileService(nil, nil, nil, vaultService, nil, nil, nil, nil, nil, guard).WithClient("obsidian", "desktop", "test")
	_, _, err = fileService.UpdateOrCreate(ctx, 1, &dto.FileUpdateRequest{Vault: "strict-vault"}, false)
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, err = fileService.Delete(ctx, 1, &dto.FileDeleteRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, err = fileService.Restore(ctx, 1, &dto.FileRestoreRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, _, err = fileService.Rename(ctx, 1, &dto.FileRenameRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)

	folderService := NewFolderService(nil, nil, nil, vaultService, nil, nil, nil, nil, guard).WithClient("obsidian", "desktop", "test")
	_, err = folderService.UpdateOrCreate(ctx, 1, &dto.FolderCreateRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, err = folderService.Delete(ctx, 1, &dto.FolderDeleteRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, err = folderService.DeleteTree(ctx, 1, &dto.FolderDeleteRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)
	_, _, err = folderService.Rename(ctx, 1, &dto.FolderRenameRequest{Vault: "strict-vault"})
	require.ErrorIs(t, err, code.ErrorSafeSyncStrictRequired)

	guardedVaultService := NewVaultService(vaultRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop(), guard)
	require.ErrorIs(t, guardedVaultService.Delete(ctx, 1, 55), code.ErrorSafeSyncStrictRequired)

	vaultRepo.AssertNumberOfCalls(t, "GetByName", 12)
}
