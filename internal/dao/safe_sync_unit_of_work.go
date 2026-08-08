package dao

import (
	"context"
	"errors"
	"fmt"
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
func (u *SafeSyncUnitOfWork) ReconcileLegacyResourceMetadata(tx *gorm.DB, vaultID int64) error {
	if tx == nil {
		return errors.New("safe sync transaction is nil")
	}
	if err := tx.Model(&model.SyncResourceMetadata{}).
		Where("vault_id = ?", vaultID).
		Update("state", "DELETED").Error; err != nil {
		return err
	}
	if err := reconcileLegacyNotes(tx, vaultID); err != nil {
		return err
	}
	if err := reconcileLegacyFiles(tx, vaultID); err != nil {
		return err
	}
	return reconcileLegacyFolders(tx, vaultID)
}

func reconcileLegacyNotes(tx *gorm.DB, vaultID int64) error {
	if !tx.Migrator().HasTable(&model.Note{}) {
		return nil
	}
	var notes []model.Note
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content").
		Where("vault_id = ?", vaultID).Find(&notes).Error; err != nil {
		return err
	}
	for _, note := range notes {
		if err := reconcileLegacyResource(tx, "NOTE", note.ID, note.VaultID, note.Action, note.Path, note.PathHash,
			util.EncodeHash32(note.Content), int64(len(note.Content))); err != nil {
			return err
		}
	}
	return nil
}

func reconcileLegacyFiles(tx *gorm.DB, vaultID int64) error {
	if !tx.Migrator().HasTable(&model.File{}) {
		return nil
	}
	var files []model.File
	if err := tx.Select("id", "vault_id", "action", "path", "path_hash", "content_hash", "size").
		Where("vault_id = ?", vaultID).Find(&files).Error; err != nil {
		return err
	}
	for _, file := range files {
		if err := reconcileLegacyResource(tx, "FILE", file.ID, file.VaultID, file.Action, file.Path, file.PathHash, file.ContentHash, file.Size); err != nil {
			return err
		}
	}
	return nil
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
