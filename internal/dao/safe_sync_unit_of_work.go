package dao

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrVaultRevisionAllocationConflict = errors.New("vault revision allocation conflict")

type SafeSyncUnitOfWork struct {
	dao             *Dao
	customPrefixKey string
}

func NewSafeSyncUnitOfWork(d *Dao) *SafeSyncUnitOfWork {
	return &SafeSyncUnitOfWork{dao: d, customPrefixKey: "user_safe_sync_"}
}

func (u *SafeSyncUnitOfWork) GetKey(uid int64) string {
	return u.customPrefixKey + strconv.FormatInt(uid, 10)
}

func (u *SafeSyncUnitOfWork) FileContentPath(uid, fileID int64) string {
	return filepath.Join(u.dao.GetFileFolderPath(uid, fileID), "file.dat")
}

func (u *SafeSyncUnitOfWork) NoteContentPath(uid, noteID int64) string {
	return filepath.Join(u.dao.GetNoteFolderPath(uid, noteID), "content.txt")
}

func init() {
	RegisterModel(ModelConfig{
		Name: "SafeSync",
		RepoFactory: func(d *Dao) daoDBCustomKey {
			return NewSafeSyncUnitOfWork(d)
		},
	})
}

func (u *SafeSyncUnitOfWork) Migrate(ctx context.Context, uid int64) error {
	return u.dao.ExecuteWrite(ctx, uid, u, func(db *gorm.DB) error {
		return model.AutoMigrateSafeSync(db)
	})
}

func (u *SafeSyncUnitOfWork) Transaction(ctx context.Context, uid int64, fn func(*gorm.DB) error) error {
	return u.dao.ExecuteWriteWithRetry(ctx, uid, u, func(db *gorm.DB) error {
		return db.Transaction(fn)
	})
}

func (u *SafeSyncUnitOfWork) IsMigrationVerified(ctx context.Context, uid int64) (bool, error) {
	db := u.dao.ResolveDB(u.GetKey(uid))
	if db == nil {
		return false, errors.New("safe sync user database is unavailable")
	}
	if !db.Migrator().HasTable(&model.SafeSyncMigrationState{}) {
		return false, nil
	}
	var state model.SafeSyncMigrationState
	err := db.WithContext(ctx).Where("id = ? AND status = ?", 1, "VERIFIED").Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state.VerifiedAt != nil && state.ReportDigest != "", nil
}

func (u *SafeSyncUnitOfWork) AllocateVaultRevisions(tx *gorm.DB, vaultID, count int64) (int64, int64, error) {
	if tx == nil {
		return 0, 0, errors.New("safe sync transaction is nil")
	}
	if count <= 0 {
		return 0, 0, fmt.Errorf("revision count must be positive: %d", count)
	}

	var state model.VaultSyncState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("vault_id = ?", vaultID).First(&state).Error; err != nil {
		return 0, 0, err
	}
	start := state.LatestVaultRevision + 1
	end := state.LatestVaultRevision + count
	result := tx.Model(&model.VaultSyncState{}).
		Where("vault_id = ? AND latest_vault_revision = ?", vaultID, state.LatestVaultRevision).
		UpdateColumn("latest_vault_revision", end)
	if result.Error != nil {
		return 0, 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, 0, ErrVaultRevisionAllocationConflict
	}
	return start, end, nil
}

func (u *SafeSyncUnitOfWork) PruneTerminalOperations(ctx context.Context, uid int64, now time.Time) (int64, error) {
	var deleted int64
	err := u.Transaction(ctx, uid, func(tx *gorm.DB) error {
		result := tx.Where("state IN ? AND expires_at <= ?", []string{"COMMITTED", "REJECTED"}, now).
			Delete(&model.SyncOperation{})
		deleted = result.RowsAffected
		return result.Error
	})
	return deleted, err
}

func (u *SafeSyncUnitOfWork) BackfillResourceMetadata(ctx context.Context, uid int64) error {
	return u.Transaction(ctx, uid, func(tx *gorm.DB) error {
		return backfillResourceMetadata(tx, nil)
	})
}

// ReconcileLegacyResourceMetadata refreshes the safe-sync baseline from the
// legacy tables after the Vault has been frozen in BOOTSTRAPPING state.
func (u *SafeSyncUnitOfWork) ReconcileLegacyResourceMetadata(tx *gorm.DB, uid, vaultID int64) error {
	if tx == nil {
		return errors.New("safe sync transaction is nil")
	}
	if err := tx.Model(&model.SyncResourceMetadata{}).
		Where("vault_id = ?", vaultID).
		Update("state", "DELETED").Error; err != nil {
		return err
	}
	if err := u.reconcileLegacyNotes(tx, uid, vaultID); err != nil {
		return err
	}
	if err := u.reconcileLegacyFiles(tx, uid, vaultID); err != nil {
		return err
	}
	return reconcileLegacyFolders(tx, vaultID)
}

// ReconcileStrictLiveContentMetadata reads the persisted content for every live
// note and attachment in a strict Vault. It updates legacy read metadata and
// returns safe resources whose content metadata needs a new revision.
func (u *SafeSyncUnitOfWork) ReconcileStrictLiveContentMetadata(tx *gorm.DB, uid, vaultID int64) ([]model.SyncResourceMetadata, error) {
	if tx == nil {
		return nil, errors.New("safe sync transaction is nil")
	}
	var resources []model.SyncResourceMetadata
	if err := tx.Where("vault_id = ? AND state = ? AND resource_type IN ?", vaultID, "LIVE", []string{"NOTE", "FILE"}).
		Order("resource_id").Find(&resources).Error; err != nil {
		return nil, err
	}
	changed := make([]model.SyncResourceMetadata, 0)
	for index := range resources {
		resource := &resources[index]
		var contentHash string
		var size int64
		switch resource.ResourceType {
		case "NOTE":
			var note model.Note
			if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content", "content_hash", "size").
				Where("id = ? AND vault_id = ? AND action <> ?", resource.LegacyID, vaultID, "delete").Take(&note).Error; err != nil {
				return nil, fmt.Errorf("load live legacy note %d: %w", resource.LegacyID, err)
			}
			if note.Path != resource.CurrentPath || note.PathHash != resource.CurrentPathHash {
				return nil, fmt.Errorf("live note %d path does not match safe resource", note.ID)
			}
			content, exists, err := u.dao.LoadContentFromFile(u.dao.GetNoteFolderPath(uid, note.ID), "content.txt")
			if err != nil {
				return nil, fmt.Errorf("read live note content for note %d: %w", note.ID, err)
			}
			if !exists {
				if note.Content == "" {
					return nil, fmt.Errorf("live note content is unavailable for note %d", note.ID)
				}
				content = note.Content
			}
			contentHash = util.EncodeHash32(content)
			size = int64(len([]byte(content)))
			if note.ContentHash != contentHash || note.Size != size {
				if err := tx.Model(&model.Note{}).Where("id = ? AND vault_id = ?", note.ID, vaultID).
					Updates(map[string]any{"content_hash": contentHash, "size": size}).Error; err != nil {
					return nil, err
				}
			}
		case "FILE":
			var file model.File
			if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content_hash", "size", "save_path").
				Where("id = ? AND vault_id = ? AND action <> ?", resource.LegacyID, vaultID, "delete").Take(&file).Error; err != nil {
				return nil, fmt.Errorf("load live legacy attachment %d: %w", resource.LegacyID, err)
			}
			if file.Path != resource.CurrentPath || file.PathHash != resource.CurrentPathHash {
				return nil, fmt.Errorf("live attachment %d path does not match safe resource", file.ID)
			}
			contentPath, err := u.resolveLiveFileContentPath(uid, &file)
			if err != nil {
				return nil, err
			}
			contentHash, size, err = hashLiveFileContent(contentPath)
			if err != nil {
				return nil, fmt.Errorf("read live attachment content for file %d: %w", file.ID, err)
			}
			if file.ContentHash != contentHash || file.Size != size {
				if err := tx.Model(&model.File{}).Where("id = ? AND vault_id = ?", file.ID, vaultID).
					Updates(map[string]any{"content_hash": contentHash, "size": size}).Error; err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("unsupported strict content resource type %q", resource.ResourceType)
		}
		if resource.ContentHash != contentHash || resource.Size != size {
			resource.ContentHash = contentHash
			resource.Size = size
			changed = append(changed, *resource)
		}
	}
	return changed, nil
}

func (u *SafeSyncUnitOfWork) reconcileLegacyNotes(tx *gorm.DB, uid, vaultID int64) error {
	if !tx.Migrator().HasTable(&model.Note{}) {
		return nil
	}
	var notes []model.Note
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content", "content_hash", "size").
		Where("vault_id = ?", vaultID).Find(&notes).Error; err != nil {
		return err
	}
	for _, note := range notes {
		contentHash, size := note.ContentHash, note.Size
		if note.Action != "delete" {
			content, exists, err := u.dao.LoadContentFromFile(u.dao.GetNoteFolderPath(uid, note.ID), "content.txt")
			if err != nil {
				return fmt.Errorf("read live note content for note %d: %w", note.ID, err)
			}
			if !exists {
				if note.Content == "" {
					return fmt.Errorf("live note content is unavailable for note %d", note.ID)
				}
				content = note.Content
			}
			contentHash = util.EncodeHash32(content)
			size = int64(len([]byte(content)))
		}
		if note.ContentHash != contentHash || note.Size != size {
			if err := tx.Model(&model.Note{}).Where("id = ? AND vault_id = ?", note.ID, vaultID).
				Updates(map[string]any{"content_hash": contentHash, "size": size}).Error; err != nil {
				return err
			}
		}
		if err := reconcileLegacyResource(tx, "NOTE", note.ID, note.VaultID, note.Action, note.Path, note.PathHash,
			contentHash, size); err != nil {
			return err
		}
	}
	return nil
}

func (u *SafeSyncUnitOfWork) reconcileLegacyFiles(tx *gorm.DB, uid, vaultID int64) error {
	if !tx.Migrator().HasTable(&model.File{}) {
		return nil
	}
	var files []model.File
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content_hash", "size", "save_path").
		Where("vault_id = ?", vaultID).Find(&files).Error; err != nil {
		return err
	}
	for _, file := range files {
		contentHash, size := file.ContentHash, file.Size
		if file.Action != "delete" {
			contentPath, err := u.resolveLiveFileContentPath(uid, &file)
			if err != nil {
				return err
			}
			contentHash, size, err = hashLiveFileContent(contentPath)
			if err != nil {
				return fmt.Errorf("read live attachment content for file %d: %w", file.ID, err)
			}
		}
		if file.ContentHash != contentHash || file.Size != size {
			if err := tx.Model(&model.File{}).Where("id = ? AND vault_id = ?", file.ID, vaultID).
				Updates(map[string]any{"content_hash": contentHash, "size": size}).Error; err != nil {
				return err
			}
		}
		if err := reconcileLegacyResource(tx, "FILE", file.ID, file.VaultID, file.Action, file.Path, file.PathHash, contentHash, size); err != nil {
			return err
		}
	}
	return nil
}

func (u *SafeSyncUnitOfWork) resolveLiveFileContentPath(uid int64, file *model.File) (string, error) {
	paths := []string{u.FileContentPath(uid, file.ID)}
	if file.SavePath != "" && file.SavePath != paths[0] {
		paths = append(paths, file.SavePath)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("live attachment content is not a regular file for file %d", file.ID)
			}
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat live attachment content for file %d: %w", file.ID, err)
		}
	}
	return "", fmt.Errorf("live attachment content is unavailable for file %d", file.ID)
}

func hashLiveFileContent(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	size := info.Size()
	if size <= util.FileHashThreshold {
		content, err := io.ReadAll(file)
		if err != nil {
			return "", 0, err
		}
		if int64(len(content)) != size {
			return "", 0, io.ErrUnexpectedEOF
		}
		return util.EncodeHash32Bytes(content), size, nil
	}

	sliceSize := int64(util.FileHashSliceSize)
	samples := make([]byte, util.FileHashSliceSize*3)
	offsets := []int64{0, size/2 - sliceSize/2, size - sliceSize}
	for index, offset := range offsets {
		start := index * util.FileHashSliceSize
		if _, err := file.ReadAt(samples[start:start+util.FileHashSliceSize], offset); err != nil {
			return "", 0, err
		}
	}
	return util.EncodeHash32Bytes(samples), size, nil
}

func reconcileLegacyFolders(tx *gorm.DB, vaultID int64) error {
	if !tx.Migrator().HasTable(&model.Folder{}) {
		return nil
	}
	var folders []model.Folder
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash").
		Where("vault_id = ?", vaultID).Find(&folders).Error; err != nil {
		return err
	}
	for _, folder := range folders {
		if err := reconcileLegacyResource(tx, "FOLDER", folder.ID, folder.VaultID, folder.Action, folder.Path, folder.PathHash, "", 0); err != nil {
			return err
		}
	}
	return nil
}

func reconcileLegacyResource(tx *gorm.DB, resourceType string, legacyID, vaultID int64, action, path, pathHash, contentHash string, size int64) error {
	state := "LIVE"
	if action == "delete" {
		state = "DELETED"
	}
	updates := map[string]any{
		"vault_id": vaultID, "current_path": path, "current_path_hash": pathHash,
		"content_hash": contentHash, "state": state, "size": size,
	}
	result := tx.Model(&model.SyncResourceMetadata{}).
		Where("resource_type = ? AND legacy_id = ?", resourceType, legacyID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	return tx.Create(&model.SyncResourceMetadata{
		ResourceID: uuid.NewString(), VaultID: vaultID, ResourceType: resourceType, LegacyID: legacyID,
		ResourceRevision: 1, CurrentPath: path, CurrentPathHash: pathHash,
		ContentHash: contentHash, State: state, Size: size,
	}).Error
}

func backfillResourceMetadata(tx *gorm.DB, verifiedAt *time.Time) error {
	vaultIDs := make(map[int64]struct{})
	if tx.Migrator().HasTable(&model.Vault{}) {
		var vaults []model.Vault
		if err := tx.Find(&vaults).Error; err != nil {
			return err
		}
		for _, vault := range vaults {
			vaultIDs[vault.ID] = struct{}{}
		}
	}

	if err := backfillNotes(tx, vaultIDs); err != nil {
		return err
	}
	if err := backfillFiles(tx, vaultIDs); err != nil {
		return err
	}
	if err := backfillFolders(tx, vaultIDs); err != nil {
		return err
	}

	for vaultID := range vaultIDs {
		state := model.VaultSyncState{VaultID: vaultID}
		if err := tx.Where("vault_id = ?", vaultID).
			Attrs(model.VaultSyncState{State: "OFF", LatestVaultRevision: 0}).
			FirstOrCreate(&state).Error; err != nil {
			return err
		}
		if verifiedAt != nil {
			if err := tx.Model(&model.VaultSyncState{}).Where("vault_id = ?", vaultID).
				Update("migration_verified_at", *verifiedAt).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func backfillNotes(tx *gorm.DB, vaultIDs map[int64]struct{}) error {
	if !tx.Migrator().HasTable(&model.Note{}) {
		return nil
	}
	var notes []model.Note
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content_hash", "size").Find(&notes).Error; err != nil {
		return err
	}
	for _, note := range notes {
		vaultIDs[note.VaultID] = struct{}{}
		if err := createBackfilledResource(tx, "NOTE", note.ID, note.VaultID, note.Action, note.Path, note.PathHash, note.ContentHash, note.Size); err != nil {
			return err
		}
	}
	return nil
}

func backfillFiles(tx *gorm.DB, vaultIDs map[int64]struct{}) error {
	if !tx.Migrator().HasTable(&model.File{}) {
		return nil
	}
	var files []model.File
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content_hash", "size").Find(&files).Error; err != nil {
		return err
	}
	for _, file := range files {
		vaultIDs[file.VaultID] = struct{}{}
		if err := createBackfilledResource(tx, "FILE", file.ID, file.VaultID, file.Action, file.Path, file.PathHash, file.ContentHash, file.Size); err != nil {
			return err
		}
	}
	return nil
}

func backfillFolders(tx *gorm.DB, vaultIDs map[int64]struct{}) error {
	if !tx.Migrator().HasTable(&model.Folder{}) {
		return nil
	}
	var folders []model.Folder
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash").Find(&folders).Error; err != nil {
		return err
	}
	for _, folder := range folders {
		vaultIDs[folder.VaultID] = struct{}{}
		if err := createBackfilledResource(tx, "FOLDER", folder.ID, folder.VaultID, folder.Action, folder.Path, folder.PathHash, "", 0); err != nil {
			return err
		}
	}
	return nil
}

func createBackfilledResource(tx *gorm.DB, resourceType string, legacyID, vaultID int64, action, path, pathHash, contentHash string, size int64) error {
	var existing model.SyncResourceMetadata
	err := tx.Where("resource_type = ? AND legacy_id = ?", resourceType, legacyID).Take(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	state := "LIVE"
	if action == "delete" {
		state = "DELETED"
	}
	resource := model.SyncResourceMetadata{
		ResourceID:       uuid.NewString(),
		VaultID:          vaultID,
		ResourceType:     resourceType,
		LegacyID:         legacyID,
		ResourceRevision: 1,
		CurrentPath:      path,
		CurrentPathHash:  pathHash,
		ContentHash:      contentHash,
		State:            state,
		Size:             size,
	}
	return tx.Create(&resource).Error
}
