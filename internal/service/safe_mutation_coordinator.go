package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SafeMutationCoordinator struct {
	uow         *dao.SafeSyncUnitOfWork
	strictGuard *StrictVaultWriteGuard
	stager      *SafeContentStager
	stagerMu    sync.Mutex
	vaultLocks  sync.Map
	filePath    func(uid, fileID int64) string
	notePath    func(uid, noteID int64) string
	now         func() time.Time
}

func NewSafeMutationCoordinator(uow *dao.SafeSyncUnitOfWork, guards ...*StrictVaultWriteGuard) *SafeMutationCoordinator {
	coordinator := &SafeMutationCoordinator{
		uow: uow, filePath: uow.FileContentPath, notePath: uow.NoteContentPath,
		now: func() time.Time { return time.Now().UTC() },
	}
	if len(guards) > 0 {
		coordinator.strictGuard = guards[0]
	}
	return coordinator
}

func (c *SafeMutationCoordinator) Mutate(ctx context.Context, uid, vaultID int64, resourceType domain.SyncResourceType, request *dto.SafeMutationRequest) (*dto.SafeMutationResponse, error) {
	unlock := c.lockVault(uid, vaultID)
	defer unlock()
	if err := c.recoverPreparedVault(ctx, uid, vaultID); err != nil {
		return nil, err
	}

	if c.strictGuard != nil {
		if err := c.strictGuard.CheckSafeWrite(ctx, uid, vaultID); err != nil {
			return nil, err
		}
	}
	if err := validateSafeMutationRequest(resourceType, request); err != nil {
		return nil, err
	}
	fingerprint, err := safeMutationFingerprint(vaultID, resourceType, request)
	if err != nil {
		return nil, err
	}
	if resourceType == domain.SyncResourceTypeNote && (request.Action == "CREATE" || request.Action == "MODIFY") {
		return c.commitContentMutation(ctx, uid, vaultID, resourceType, request, []byte(request.Content), fingerprint)
	}

	var result *dto.SafeMutationResponse
	var terminalErr error
	err = c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := requireStrictVaultTransaction(tx, vaultID); err != nil {
			terminalErr = err
			return nil
		}
		if err := enforceDeviceWriteRole(tx, vaultID, request.DeviceID, c.now()); err != nil {
			terminalErr = err
			return nil
		}

		existing, err := findSafeSyncOperation(tx, vaultID, request.DeviceID, request.OperationID)
		if err != nil {
			return err
		}
		if existing != nil {
			result, terminalErr = replaySafeSyncOperation(existing, fingerprint, c.now())
			return nil
		}
		if err := ensureNoPreparedVaultOperation(tx, vaultID); err != nil {
			terminalErr = err
			return nil
		}

		if resourceType == domain.SyncResourceTypeFolder && (request.Action == "RENAME" || request.Action == "DELETE") {
			result, err = c.mutateFolderTree(tx, vaultID, request, fingerprint)
		} else {
			result, err = c.mutateSingleResource(tx, vaultID, resourceType, request, fingerprint)
		}
		if err == nil {
			return nil
		}
		var safeErr *domain.SafeSyncError
		if !errors.As(err, &safeErr) {
			return err
		}
		terminalErr = err
		return createRejectedSafeSyncOperation(tx, vaultID, request, resourceType, fingerprint, safeErr.Code, c.now())
	})
	if err != nil {
		return nil, err
	}
	if terminalErr != nil {
		return nil, terminalErr
	}
	return result, nil
}

func (c *SafeMutationCoordinator) ValidateFileUpload(ctx context.Context, uid, vaultID int64, request *dto.SafeMutationRequest) error {
	unlock := c.lockVault(uid, vaultID)
	defer unlock()
	if err := c.recoverPreparedVault(ctx, uid, vaultID); err != nil {
		return err
	}
	return c.validateFileUpload(ctx, uid, vaultID, request)
}

func (c *SafeMutationCoordinator) validateFileUpload(ctx context.Context, uid, vaultID int64, request *dto.SafeMutationRequest) error {
	if c.strictGuard != nil {
		if err := c.strictGuard.CheckSafeWrite(ctx, uid, vaultID); err != nil {
			return err
		}
	}
	if err := validateSafeMutationRequest(domain.SyncResourceTypeFile, request); err != nil {
		return err
	}
	if request.Action != "CREATE" && request.Action != "MODIFY" {
		return errors.New("safe file upload only supports create or modify")
	}
	return nil
}

func (c *SafeMutationCoordinator) CommitFile(ctx context.Context, uid, vaultID int64, request *dto.SafeMutationRequest, uploadPath string) (*dto.SafeMutationResponse, error) {
	unlock := c.lockVault(uid, vaultID)
	defer unlock()
	if err := c.recoverPreparedVault(ctx, uid, vaultID); err != nil {
		return nil, err
	}
	if err := c.validateFileUpload(ctx, uid, vaultID, request); err != nil {
		return nil, err
	}
	fingerprint, err := safeMutationFingerprint(vaultID, domain.SyncResourceTypeFile, request)
	if err != nil {
		return nil, err
	}
	if replayed, found, err := c.replayExistingContentOperation(ctx, uid, vaultID, request, fingerprint); err != nil {
		return nil, err
	} else if found {
		return replayed, nil
	}
	content, err := os.ReadFile(uploadPath)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != request.Size {
		return nil, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "uploaded file size does not match commit")
	}
	if !matchesSafeContentHash(content, request.ContentHash) {
		return nil, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "uploaded file hash does not match commit")
	}
	return c.commitContentMutation(ctx, uid, vaultID, domain.SyncResourceTypeFile, request, content, fingerprint)
}

func (c *SafeMutationCoordinator) commitContentMutation(
	ctx context.Context,
	uid, vaultID int64,
	resourceType domain.SyncResourceType,
	request *dto.SafeMutationRequest,
	content []byte,
	fingerprint string,
) (*dto.SafeMutationResponse, error) {
	if resourceType != domain.SyncResourceTypeNote && resourceType != domain.SyncResourceTypeFile {
		return nil, fmt.Errorf("unsupported safe content resource type: %s", resourceType)
	}
	if request.Action != "CREATE" && request.Action != "MODIFY" {
		return nil, errors.New("safe content mutation only supports create or modify")
	}
	stager, err := c.contentStager()
	if err != nil {
		return nil, err
	}

	var prepared *model.SyncOperation
	var newlyStaged *StagedContent
	var result *dto.SafeMutationResponse
	var terminalErr error
	err = c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := requireStrictVaultTransaction(tx, vaultID); err != nil {
			terminalErr = err
			return nil
		}
		if err := enforceDeviceWriteRole(tx, vaultID, request.DeviceID, c.now()); err != nil {
			terminalErr = err
			return nil
		}
		existing, err := findSafeSyncOperation(tx, vaultID, request.DeviceID, request.OperationID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.State == string(domain.SyncOperationStatePrepared) {
				if existing.RequestFingerprint != fingerprint {
					terminalErr = newSafeSyncError(domain.SafeSyncErrorOperationIDReused, "operationId is bound to a different request")
					return nil
				}
				prepared = existing
				return nil
			}
			result, terminalErr = replaySafeSyncOperation(existing, fingerprint, c.now())
			return nil
		}
		if err := ensureNoPreparedVaultOperation(tx, vaultID); err != nil {
			terminalErr = err
			return nil
		}

		resource, create, err := prepareSingleSafeResource(tx, vaultID, resourceType, request)
		if err != nil {
			var safeErr *domain.SafeSyncError
			if !errors.As(err, &safeErr) {
				return err
			}
			terminalErr = err
			return createRejectedSafeSyncOperation(tx, vaultID, request, resourceType, fingerprint, safeErr.Code, c.now())
		}
		legacyID := resource.LegacyID
		if create {
			legacyID, err = reserveSafeContentLegacyID(tx, resourceType)
			if err != nil {
				return err
			}
		}
		targetPath, err := c.contentTargetPath(uid, resourceType, legacyID)
		if err != nil {
			return err
		}
		staged, err := stager.Stage(ctx, request.OperationID, targetPath, content, request.ContentHash)
		if err != nil {
			return err
		}
		newlyStaged = staged
		payload, err := json.Marshal(request)
		if err != nil {
			return err
		}
		prepared = &model.SyncOperation{
			VaultID: vaultID, DeviceID: request.DeviceID, OperationID: request.OperationID,
			Action: string(resourceType) + ":" + request.Action, RequestFingerprint: fingerprint,
			State: string(domain.SyncOperationStatePrepared), ResourceID: resource.ResourceID,
			LegacyID: legacyID, RequestPayload: string(payload), StagedPath: staged.StagedPath,
			OldImagePath: staged.OldImagePath, TargetPath: staged.TargetPath,
			ExpectedHash: staged.ExpectedHash, TargetExisted: staged.TargetExisted,
			ExpiresAt: c.now().Add(model.SafeSyncOperationRetention),
		}
		return tx.Create(prepared).Error
	})
	if err != nil {
		if newlyStaged != nil {
			_ = stager.Finalize(newlyStaged)
		}
		return nil, err
	}
	if terminalErr != nil {
		return nil, terminalErr
	}
	if result != nil {
		return result, nil
	}
	if prepared == nil {
		return nil, errors.New("safe content operation was not prepared")
	}

	staged := stagedContentFromOperation(prepared)
	if err := stager.Apply(ctx, staged); err != nil {
		return nil, err
	}
	result, err = c.commitPreparedContent(ctx, uid, prepared)
	if err != nil {
		_, _ = stager.Recover(context.Background(), staged)
		return nil, err
	}
	if err := stager.Finalize(staged); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *SafeMutationCoordinator) contentTargetPath(uid int64, resourceType domain.SyncResourceType, legacyID int64) (string, error) {
	switch resourceType {
	case domain.SyncResourceTypeNote:
		return c.notePath(uid, legacyID), nil
	case domain.SyncResourceTypeFile:
		return c.filePath(uid, legacyID), nil
	default:
		return "", fmt.Errorf("unsupported safe content resource type: %s", resourceType)
	}
}

func preparedContentResourceType(action string) (domain.SyncResourceType, error) {
	prefix, mutation, found := strings.Cut(action, ":")
	if !found || (mutation != "CREATE" && mutation != "MODIFY") {
		return "", fmt.Errorf("unsupported prepared safe sync action %q", action)
	}
	resourceType := domain.SyncResourceType(prefix)
	if resourceType != domain.SyncResourceTypeNote && resourceType != domain.SyncResourceTypeFile {
		return "", fmt.Errorf("unsupported prepared safe sync action %q", action)
	}
	return resourceType, nil
}

func (c *SafeMutationCoordinator) replayExistingContentOperation(ctx context.Context, uid, vaultID int64, request *dto.SafeMutationRequest, fingerprint string) (*dto.SafeMutationResponse, bool, error) {
	var result *dto.SafeMutationResponse
	var found bool
	var terminalErr error
	err := c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := requireStrictVaultTransaction(tx, vaultID); err != nil {
			terminalErr = err
			return nil
		}
		existing, err := findSafeSyncOperation(tx, vaultID, request.DeviceID, request.OperationID)
		if err != nil {
			return err
		}
		if existing == nil {
			return nil
		}
		found = true
		result, terminalErr = replaySafeSyncOperation(existing, fingerprint, c.now())
		return nil
	})
	if err != nil {
		return nil, found, err
	}
	return result, found, terminalErr
}

func (c *SafeMutationCoordinator) RecoverPrepared(ctx context.Context, uid int64) error {
	var vaultIDs []int64
	if err := c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		return tx.Model(&model.SyncOperation{}).
			Where("state = ?", string(domain.SyncOperationStatePrepared)).
			Distinct("vault_id").Order("vault_id").Pluck("vault_id", &vaultIDs).Error
	}); err != nil {
		return err
	}
	for _, vaultID := range vaultIDs {
		unlock := c.lockVault(uid, vaultID)
		err := c.recoverPreparedVault(ctx, uid, vaultID)
		unlock()
		if err != nil {
			return fmt.Errorf("recover prepared safe sync operation for vault %d: %w", vaultID, err)
		}
	}
	return nil
}

// RepairLegacyHierarchy restores the legacy FID relationships consumed by the
// WebGUI without changing safe-sync resources, revisions, or content.
func (c *SafeMutationCoordinator) RepairLegacyHierarchy(ctx context.Context, uid int64) error {
	var vaultIDs []int64
	if err := c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		return tx.Model(&model.VaultSyncState{}).
			Where("state = ?", "STRICT").Order("vault_id").Pluck("vault_id", &vaultIDs).Error
	}); err != nil {
		return err
	}
	for _, vaultID := range vaultIDs {
		unlock := c.lockVault(uid, vaultID)
		err := c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
			return repairSafeLegacyHierarchy(tx, vaultID)
		})
		unlock()
		if err != nil {
			return fmt.Errorf("repair safe sync hierarchy for vault %d: %w", vaultID, err)
		}
	}
	return nil
}

func (c *SafeMutationCoordinator) recoverPreparedVault(ctx context.Context, uid, vaultID int64) error {
	var operations []model.SyncOperation
	if err := c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		return tx.Where("vault_id = ? AND state = ?", vaultID, string(domain.SyncOperationStatePrepared)).
			Order("id").Find(&operations).Error
	}); err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}
	stager, err := c.contentStager()
	if err != nil {
		return err
	}
	for index := range operations {
		operation := &operations[index]
		if _, err := preparedContentResourceType(operation.Action); err != nil {
			return fmt.Errorf("unsupported prepared safe sync operation %d", operation.ID)
		}
		staged := stagedContentFromOperation(operation)
		recovery, err := stager.Recover(ctx, staged)
		if err != nil {
			return err
		}
		switch recovery {
		case SafeContentRecoveryApplied:
			if _, err := c.commitPreparedContent(ctx, uid, operation); err != nil {
				return err
			}
		case SafeContentRecoveryRestored:
			if err := c.discardPreparedContent(ctx, uid, operation.ID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported safe content recovery result %q", recovery)
		}
		if err := stager.Finalize(staged); err != nil {
			return err
		}
	}
	return nil
}

func (c *SafeMutationCoordinator) discardPreparedContent(ctx context.Context, uid, operationID int64) error {
	return c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND state = ?", operationID, string(domain.SyncOperationStatePrepared)).
			Delete(&model.SyncOperation{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("prepared content operation was not discarded")
		}
		return nil
	})
}

func (c *SafeMutationCoordinator) lockVault(uid, vaultID int64) func() {
	value, _ := c.vaultLocks.LoadOrStore([2]int64{uid, vaultID}, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (c *SafeMutationCoordinator) contentStager() (*SafeContentStager, error) {
	c.stagerMu.Lock()
	defer c.stagerMu.Unlock()
	if c.stager != nil {
		return c.stager, nil
	}
	stager, err := NewSafeContentStager(filepath.Join("storage", ".safe-sync-staging"), "storage")
	if err != nil {
		return nil, err
	}
	c.stager = stager
	return stager, nil
}

func (c *SafeMutationCoordinator) commitPreparedContent(ctx context.Context, uid int64, prepared *model.SyncOperation) (*dto.SafeMutationResponse, error) {
	resourceType, err := preparedContentResourceType(prepared.Action)
	if err != nil {
		return nil, err
	}
	var request dto.SafeMutationRequest
	if err := json.Unmarshal([]byte(prepared.RequestPayload), &request); err != nil {
		return nil, err
	}
	var result *dto.SafeMutationResponse
	err = c.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		var operation model.SyncOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", prepared.ID).Take(&operation).Error; err != nil {
			return err
		}
		if operation.State == string(domain.SyncOperationStateCommitted) {
			var err error
			result, err = replaySafeSyncOperation(&operation, prepared.RequestFingerprint, c.now())
			return err
		}
		if operation.State != string(domain.SyncOperationStatePrepared) || operation.RequestFingerprint != prepared.RequestFingerprint || operation.Action != prepared.Action {
			return newSafeSyncError(domain.SafeSyncErrorOperationIDReused, "prepared content operation changed")
		}
		if !matchesFileHash(operation.TargetPath, operation.ExpectedHash) {
			return errors.New("prepared content is not applied")
		}
		if err := requireStrictVaultTransaction(tx, operation.VaultID); err != nil {
			return err
		}

		resource, create, err := preparePreparedSafeContentResource(tx, operation.VaultID, resourceType, &request, &operation)
		if err != nil {
			return err
		}
		if err := applyPreparedSafeContentRecord(tx, resourceType, resource, &request, create); err != nil {
			return err
		}
		if create {
			if err := tx.Create(resource).Error; err != nil {
				return err
			}
		} else if err := tx.Save(resource).Error; err != nil {
			return err
		}
		_, vaultRevision, err := c.uow.AllocateVaultRevisions(tx, operation.VaultID, 1)
		if err != nil {
			return err
		}
		transactionID := uuid.NewString()
		if err := createSafeSyncEvent(tx, resource, request.Action, "", vaultRevision, transactionID, request.OperationID); err != nil {
			return err
		}
		result = &dto.SafeMutationResponse{
			ResourceID: resource.ResourceID, ResourceRevision: resource.ResourceRevision,
			VaultRevision: vaultRevision, ContentHash: resource.ContentHash, Outcome: safeMutationOutcome(request.Action),
		}
		updates := map[string]any{
			"state": string(domain.SyncOperationStateCommitted), "resource_revision": result.ResourceRevision,
			"vault_revision": result.VaultRevision, "content_hash": result.ContentHash, "outcome": result.Outcome,
			"request_payload": "", "staged_path": "", "old_image_path": "", "target_path": "", "expected_hash": "",
		}
		changed := tx.Model(&model.SyncOperation{}).Where("id = ? AND state = ?", operation.ID, string(domain.SyncOperationStatePrepared)).Updates(updates)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return errors.New("prepared content operation was not committed")
		}
		return nil
	})
	return result, err
}

func preparePreparedSafeContentResource(tx *gorm.DB, vaultID int64, resourceType domain.SyncResourceType, request *dto.SafeMutationRequest, operation *model.SyncOperation) (*model.SyncResourceMetadata, bool, error) {
	if request.Action != "CREATE" {
		resource, _, err := prepareSingleSafeResource(tx, vaultID, resourceType, request)
		if err != nil {
			return nil, false, err
		}
		if resource.LegacyID != operation.LegacyID || resource.ResourceID != operation.ResourceID {
			return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "prepared content resource identity changed")
		}
		return resource, false, nil
	}

	var count int64
	if err := tx.Model(&model.SyncResourceMetadata{}).
		Where("vault_id = ? AND state = ? AND current_path = ?", vaultID, string(domain.SyncResourceStateLive), request.Path).
		Count(&count).Error; err != nil {
		return nil, false, err
	}
	if count > 0 {
		return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "create target path is live")
	}
	return &model.SyncResourceMetadata{
		ResourceID: operation.ResourceID, VaultID: vaultID, ResourceType: string(resourceType),
		LegacyID: operation.LegacyID, ResourceRevision: 1, CurrentPath: request.Path,
		CurrentPathHash: request.PathHash, ContentHash: request.ContentHash,
		State: string(domain.SyncResourceStateLive), Size: request.Size,
	}, true, nil
}

func applyPreparedSafeContentRecord(tx *gorm.DB, resourceType domain.SyncResourceType, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) error {
	switch resourceType {
	case domain.SyncResourceTypeNote:
		return applyPreparedSafeNoteRecord(tx, resource, request, create)
	case domain.SyncResourceTypeFile:
		return applyPreparedSafeFileRecord(tx, resource, request, create)
	default:
		return fmt.Errorf("unsupported prepared content resource type: %s", resourceType)
	}
}

func applyPreparedSafeNoteRecord(tx *gorm.DB, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) error {
	now := timex.Now()
	fid, err := safeLegacyParentFID(tx, resource.VaultID, request.Path)
	if err != nil {
		return err
	}
	if create {
		return tx.Create(&model.Note{
			ID: resource.LegacyID, VaultID: resource.VaultID, Action: "create", FID: fid, Path: request.Path,
			PathHash: request.PathHash, Content: "", ContentHash: request.ContentHash, Version: 1, Size: request.Size,
			Ctime: request.Ctime, Mtime: request.Mtime, UpdatedTimestamp: now.UnixMilli(), CreatedAt: now, UpdatedAt: now,
		}).Error
	}
	var note model.Note
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND vault_id = ?", resource.LegacyID, resource.VaultID).Take(&note).Error; err != nil {
		return err
	}
	note.Action = "modify"
	note.FID = fid
	note.Path = request.Path
	note.PathHash = request.PathHash
	note.Content = ""
	note.ContentHash = request.ContentHash
	note.Size = request.Size
	note.Ctime = request.Ctime
	note.Mtime = request.Mtime
	note.Version++
	note.Rename = 0
	note.UpdatedTimestamp = now.UnixMilli()
	note.UpdatedAt = now
	return tx.Save(&note).Error
}

func applyPreparedSafeFileRecord(tx *gorm.DB, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) error {
	now := timex.Now()
	fid, err := safeLegacyParentFID(tx, resource.VaultID, request.Path)
	if err != nil {
		return err
	}
	if create {
		return tx.Create(&model.File{
			ID: resource.LegacyID, VaultID: resource.VaultID, Action: "create", FID: fid, Path: request.Path,
			PathHash: request.PathHash, ContentHash: request.ContentHash, Size: request.Size,
			Ctime: request.Ctime, Mtime: request.Mtime, UpdatedTimestamp: now.UnixMilli(), CreatedAt: now, UpdatedAt: now,
		}).Error
	}
	var file model.File
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND vault_id = ?", resource.LegacyID, resource.VaultID).Take(&file).Error; err != nil {
		return err
	}
	file.Action = "modify"
	file.FID = fid
	file.Path = request.Path
	file.PathHash = request.PathHash
	file.ContentHash = request.ContentHash
	file.Size = request.Size
	file.Ctime = request.Ctime
	file.Mtime = request.Mtime
	file.Rename = 0
	file.UpdatedTimestamp = now.UnixMilli()
	file.UpdatedAt = now
	return tx.Save(&file).Error
}

func ensureNoPreparedVaultOperation(tx *gorm.DB, vaultID int64) error {
	var count int64
	if err := tx.Model(&model.SyncOperation{}).Where("vault_id = ? AND state = ?", vaultID, string(domain.SyncOperationStatePrepared)).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "a prepared operation requires recovery")
	}
	return nil
}

func reserveSafeContentLegacyID(tx *gorm.DB, resourceType domain.SyncResourceType) (int64, error) {
	switch resourceType {
	case domain.SyncResourceTypeNote:
		return reserveSafeNoteLegacyID(tx)
	case domain.SyncResourceTypeFile:
		return reserveSafeFileLegacyID(tx)
	default:
		return 0, fmt.Errorf("unsupported safe content resource type: %s", resourceType)
	}
}

func reserveSafeNoteLegacyID(tx *gorm.DB) (int64, error) {
	var id int64
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Raw("SELECT nextval(pg_get_serial_sequence('note', 'id'))").Scan(&id).Error; err != nil {
			return 0, err
		}
		return id, nil
	}
	if err := tx.Model(&model.Note{}).Select("COALESCE(MAX(id), 0) + 1").Scan(&id).Error; err != nil {
		return 0, err
	}
	return id, nil
}

func reserveSafeFileLegacyID(tx *gorm.DB) (int64, error) {
	var id int64
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Raw("SELECT nextval(pg_get_serial_sequence('file', 'id'))").Scan(&id).Error; err != nil {
			return 0, err
		}
		return id, nil
	}
	if err := tx.Model(&model.File{}).Select("COALESCE(MAX(id), 0) + 1").Scan(&id).Error; err != nil {
		return 0, err
	}
	return id, nil
}

func stagedContentFromOperation(operation *model.SyncOperation) *StagedContent {
	return &StagedContent{
		OperationID: operation.OperationID, TargetPath: operation.TargetPath, StagedPath: operation.StagedPath,
		OldImagePath: operation.OldImagePath, ExpectedHash: operation.ExpectedHash, TargetExisted: operation.TargetExisted,
	}
}

func (c *SafeMutationCoordinator) mutateSingleResource(tx *gorm.DB, vaultID int64, resourceType domain.SyncResourceType, request *dto.SafeMutationRequest, fingerprint string) (*dto.SafeMutationResponse, error) {
	resource, isCreate, err := prepareSingleSafeResource(tx, vaultID, resourceType, request)
	if err != nil {
		return nil, err
	}
	previousPath := resource.CurrentPath
	if isCreate {
		previousPath = ""
	}

	legacyID, err := applySingleLegacyMutation(tx, vaultID, resourceType, resource, request, isCreate)
	if err != nil {
		return nil, err
	}
	if isCreate {
		resource.LegacyID = legacyID
		if err := tx.Create(resource).Error; err != nil {
			return nil, err
		}
	} else if err := tx.Save(resource).Error; err != nil {
		return nil, err
	}

	_, vaultRevision, err := c.uow.AllocateVaultRevisions(tx, vaultID, 1)
	if err != nil {
		return nil, err
	}
	transactionID := uuid.NewString()
	if previousPath != "" && (request.Action == "RENAME" || request.Action == "DELETE") {
		if err := createSafePathTombstone(tx, resource, previousPath, vaultRevision, transactionID); err != nil {
			return nil, err
		}
	}
	if err := createSafeSyncEvent(tx, resource, request.Action, previousPath, vaultRevision, transactionID, request.OperationID); err != nil {
		return nil, err
	}
	result := &dto.SafeMutationResponse{
		ResourceID: resource.ResourceID, ResourceRevision: resource.ResourceRevision,
		VaultRevision: vaultRevision, ContentHash: resource.ContentHash, Outcome: safeMutationOutcome(request.Action),
	}
	if err := createCommittedSafeSyncOperation(tx, vaultID, request, resourceType, fingerprint, result, c.now()); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *SafeMutationCoordinator) mutateFolderTree(tx *gorm.DB, vaultID int64, request *dto.SafeMutationRequest, fingerprint string) (*dto.SafeMutationResponse, error) {
	_, _, err := prepareSingleSafeResource(tx, vaultID, domain.SyncResourceTypeFolder, request)
	if err != nil {
		return nil, err
	}
	oldRoot := request.PreviousPath
	order := "current_path"
	if request.Action == "DELETE" {
		oldRoot = request.Path
		order = "current_path DESC"
	}
	var resources []model.SyncResourceMetadata
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("vault_id = ? AND state = ? AND (current_path = ? OR current_path LIKE ?)", vaultID, string(domain.SyncResourceStateLive), oldRoot, oldRoot+"/%").
		Order(order).Find(&resources).Error; err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		return nil, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "folder tree is empty")
	}

	affectedIDs := make([]string, 0, len(resources))
	targetPaths := make([]string, 0, len(resources))
	for _, resource := range resources {
		affectedIDs = append(affectedIDs, resource.ResourceID)
		if request.Action == "RENAME" {
			targetPaths = append(targetPaths, replaceSafePathPrefix(resource.CurrentPath, oldRoot, request.Path))
		}
	}
	if request.Action == "RENAME" {
		var conflicts int64
		if err := tx.Model(&model.SyncResourceMetadata{}).
			Where("vault_id = ? AND state = ? AND current_path IN ? AND resource_id NOT IN ?", vaultID, string(domain.SyncResourceStateLive), targetPaths, affectedIDs).
			Count(&conflicts).Error; err != nil {
			return nil, err
		}
		if conflicts > 0 {
			return nil, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "folder rename target contains live resources")
		}
	}

	startRevision, endRevision, err := c.uow.AllocateVaultRevisions(tx, vaultID, int64(len(resources)))
	if err != nil {
		return nil, err
	}
	transactionID := uuid.NewString()
	var rootResult *dto.SafeMutationResponse
	for index := range resources {
		resource := &resources[index]
		oldPath := resource.CurrentPath
		if request.Action == "RENAME" {
			resource.CurrentPath = replaceSafePathPrefix(oldPath, oldRoot, request.Path)
			resource.CurrentPathHash = util.EncodeHash32(resource.CurrentPath)
		} else {
			resource.State = string(domain.SyncResourceStateDeleted)
		}
		resource.ResourceRevision++
		if err := applyLegacyTreeMutation(tx, resource, request.Action); err != nil {
			return nil, err
		}
		if err := tx.Save(resource).Error; err != nil {
			return nil, err
		}
		vaultRevision := startRevision + int64(index)
		if err := createSafePathTombstone(tx, resource, oldPath, vaultRevision, transactionID); err != nil {
			return nil, err
		}
		if err := createSafeSyncEvent(tx, resource, request.Action, oldPath, vaultRevision, transactionID, request.OperationID); err != nil {
			return nil, err
		}
		if resource.ResourceID == request.ResourceID {
			rootResult = &dto.SafeMutationResponse{
				ResourceID: resource.ResourceID, ResourceRevision: resource.ResourceRevision,
				VaultRevision: endRevision, ContentHash: resource.ContentHash, Outcome: safeMutationOutcome(request.Action),
			}
		}
	}
	if rootResult == nil {
		return nil, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "folder root is not part of its resource tree")
	}
	if err := createCommittedSafeSyncOperation(tx, vaultID, request, domain.SyncResourceTypeFolder, fingerprint, rootResult, c.now()); err != nil {
		return nil, err
	}
	return rootResult, nil
}

func prepareSingleSafeResource(tx *gorm.DB, vaultID int64, resourceType domain.SyncResourceType, request *dto.SafeMutationRequest) (*model.SyncResourceMetadata, bool, error) {
	if request.Action == "CREATE" {
		var count int64
		if err := tx.Model(&model.SyncResourceMetadata{}).
			Where("vault_id = ? AND state = ? AND current_path = ?", vaultID, string(domain.SyncResourceStateLive), request.Path).
			Count(&count).Error; err != nil {
			return nil, false, err
		}
		if count > 0 {
			return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "create target path is live")
		}
		return &model.SyncResourceMetadata{
			ResourceID: uuid.NewString(), VaultID: vaultID, ResourceType: string(resourceType),
			ResourceRevision: 1, CurrentPath: request.Path, CurrentPathHash: request.PathHash,
			ContentHash: request.ContentHash, State: string(domain.SyncResourceStateLive), Size: request.Size,
		}, true, nil
	}

	var resource model.SyncResourceMetadata
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id = ?", request.ResourceID).Take(&resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "resource does not exist")
	}
	if err != nil {
		return nil, false, err
	}
	if resource.VaultID != vaultID || resource.ResourceType != string(resourceType) || resource.State != string(domain.SyncResourceStateLive) {
		return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "resource identity or state does not match")
	}
	if resource.ResourceRevision != request.BaseRevision {
		return nil, false, newSafeSyncError(domain.SafeSyncErrorRevisionConflict, fmt.Sprintf("expected revision %d, actual %d", request.BaseRevision, resource.ResourceRevision))
	}
	if request.BaseHash != "" && resource.ContentHash != request.BaseHash {
		return nil, false, newSafeSyncError(domain.SafeSyncErrorRevisionConflict, "base content hash does not match")
	}
	expectedPath := request.Path
	expectedPathHash := request.PathHash
	if request.Action == "RENAME" {
		expectedPath = request.PreviousPath
		expectedPathHash = request.PreviousPathHash
	}
	if resource.CurrentPath != expectedPath || (expectedPathHash != "" && resource.CurrentPathHash != expectedPathHash) {
		return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "resource path does not match")
	}
	if request.Action == "RENAME" {
		var count int64
		if err := tx.Model(&model.SyncResourceMetadata{}).
			Where("vault_id = ? AND state = ? AND current_path = ? AND resource_id <> ?", vaultID, string(domain.SyncResourceStateLive), request.Path, resource.ResourceID).
			Count(&count).Error; err != nil {
			return nil, false, err
		}
		if count > 0 {
			return nil, false, newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "rename target path is live")
		}
		resource.CurrentPath = request.Path
		resource.CurrentPathHash = request.PathHash
	}
	if request.Action == "MODIFY" {
		resource.ContentHash = request.ContentHash
		resource.Size = request.Size
	}
	if request.Action == "DELETE" {
		resource.State = string(domain.SyncResourceStateDeleted)
	}
	resource.ResourceRevision++
	return &resource, false, nil
}

func applySingleLegacyMutation(tx *gorm.DB, vaultID int64, resourceType domain.SyncResourceType, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) (int64, error) {
	switch resourceType {
	case domain.SyncResourceTypeNote:
		if create || request.Action == "MODIFY" {
			return 0, errors.New("safe note content mutations require prepared content")
		}
		return applySafeNoteMutation(tx, vaultID, resource, request, create)
	case domain.SyncResourceTypeFolder:
		return applySafeFolderMutation(tx, vaultID, resource, request, create)
	case domain.SyncResourceTypeFile:
		if create || request.Action == "MODIFY" {
			return 0, errors.New("safe file content mutations require upload commit")
		}
		return applySafeFileMutation(tx, vaultID, resource, request, create)
	default:
		return 0, fmt.Errorf("unsupported safe resource type: %s", resourceType)
	}
}

func applySafeNoteMutation(tx *gorm.DB, vaultID int64, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) (int64, error) {
	now := timex.Now()
	if create || request.Action == "MODIFY" {
		return 0, errors.New("safe note content mutations require prepared content")
	}
	var note model.Note
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND vault_id = ?", resource.LegacyID, vaultID).Take(&note).Error; err != nil {
		return 0, err
	}
	switch request.Action {
	case "DELETE":
		note.Action = "delete"
		note.Rename = 0
	case "RENAME":
		fid, err := safeLegacyParentFID(tx, vaultID, request.Path)
		if err != nil {
			return 0, err
		}
		note.FID = fid
		note.Path = request.Path
		note.PathHash = request.PathHash
		note.Action = "modify"
		note.Rename = 0
	}
	note.UpdatedTimestamp = now.UnixMilli()
	note.UpdatedAt = now
	return note.ID, tx.Save(&note).Error
}

func applySafeFileMutation(tx *gorm.DB, vaultID int64, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) (int64, error) {
	if create || request.Action == "MODIFY" {
		return 0, errors.New("safe file content mutations require upload commit")
	}
	now := timex.Now()
	var file model.File
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND vault_id = ?", resource.LegacyID, vaultID).Take(&file).Error; err != nil {
		return 0, err
	}
	switch request.Action {
	case "DELETE":
		file.Action = "delete"
		file.Rename = 0
	case "RENAME":
		fid, err := safeLegacyParentFID(tx, vaultID, request.Path)
		if err != nil {
			return 0, err
		}
		file.FID = fid
		file.Path = request.Path
		file.PathHash = request.PathHash
		file.Action = "modify"
		file.Rename = 0
	}
	file.UpdatedTimestamp = now.UnixMilli()
	file.UpdatedAt = now
	return file.ID, tx.Save(&file).Error
}

func applySafeFolderMutation(tx *gorm.DB, vaultID int64, resource *model.SyncResourceMetadata, request *dto.SafeMutationRequest, create bool) (int64, error) {
	now := timex.Now()
	fid, err := safeLegacyParentFID(tx, vaultID, request.Path)
	if err != nil {
		return 0, err
	}
	level := safeLegacyFolderLevel(request.Path)
	if create {
		folder := &model.Folder{
			VaultID: vaultID, Action: "create", Path: request.Path, PathHash: request.PathHash,
			Level: level, FID: fid, Ctime: request.Ctime, Mtime: request.Mtime,
			UpdatedTimestamp: now.UnixMilli(), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(folder).Error; err != nil {
			return 0, err
		}
		return folder.ID, nil
	}
	var folder model.Folder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND vault_id = ?", resource.LegacyID, vaultID).Take(&folder).Error; err != nil {
		return 0, err
	}
	folder.Path = resource.CurrentPath
	folder.PathHash = resource.CurrentPathHash
	folder.Level = level
	folder.FID = fid
	folder.Action = "create"
	if request.Action == "DELETE" {
		folder.Action = "delete"
	}
	folder.UpdatedTimestamp = now.UnixMilli()
	folder.UpdatedAt = now
	return folder.ID, tx.Save(&folder).Error
}

func applyLegacyTreeMutation(tx *gorm.DB, resource *model.SyncResourceMetadata, action string) error {
	updates := map[string]any{
		"path": resource.CurrentPath, "path_hash": resource.CurrentPathHash,
		"updated_timestamp": time.Now().UnixMilli(), "rename": 0,
	}
	if action == "DELETE" {
		updates["action"] = "delete"
	} else {
		fid, err := safeLegacyParentFID(tx, resource.VaultID, resource.CurrentPath)
		if err != nil {
			return err
		}
		updates["fid"] = fid
		if resource.ResourceType == string(domain.SyncResourceTypeFolder) {
			updates["action"] = "create"
			updates["level"] = safeLegacyFolderLevel(resource.CurrentPath)
		} else {
			updates["action"] = "modify"
		}
	}
	var target any
	switch domain.SyncResourceType(resource.ResourceType) {
	case domain.SyncResourceTypeNote:
		target = &model.Note{}
	case domain.SyncResourceTypeFile:
		target = &model.File{}
	case domain.SyncResourceTypeFolder:
		target = &model.Folder{}
		delete(updates, "rename")
	default:
		return fmt.Errorf("unsupported tree resource type: %s", resource.ResourceType)
	}
	result := tx.Model(target).Where("id = ? AND vault_id = ?", resource.LegacyID, resource.VaultID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("legacy %s resource %d was not updated", resource.ResourceType, resource.LegacyID)
	}
	return nil
}

func safeLegacyParentFID(tx *gorm.DB, vaultID int64, path string) (int64, error) {
	parent := parentPath(strings.Trim(path, "/"))
	if parent == "" {
		return 0, nil
	}
	var folder model.Folder
	err := tx.Where("vault_id = ? AND path = ? AND action <> ?", vaultID, parent, "delete").Order("id").Take(&folder).Error
	if err != nil && !tx.Migrator().HasTable(&model.Folder{}) {
		return 0, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("live parent folder %q is missing", parent)
	}
	if err != nil {
		return 0, err
	}
	return folder.ID, nil
}

func safeLegacyFolderLevel(path string) int64 {
	path = strings.Trim(path, "/")
	if path == "" {
		return 0
	}
	return int64(strings.Count(path, "/") + 1)
}

func repairSafeLegacyHierarchy(tx *gorm.DB, vaultID int64) error {
	if !tx.Migrator().HasTable(&model.Folder{}) {
		return nil
	}
	var folders []model.Folder
	if err := tx.Where("vault_id = ?", vaultID).Order("path, id").Find(&folders).Error; err != nil {
		return err
	}
	canonicalFolderID := make(map[string]int64)
	for _, folder := range folders {
		if folder.Action == "delete" {
			continue
		}
		if _, exists := canonicalFolderID[folder.Path]; !exists {
			canonicalFolderID[folder.Path] = folder.ID
		}
	}
	resolveParent := func(path string) (int64, error) {
		parent := parentPath(strings.Trim(path, "/"))
		if parent == "" {
			return 0, nil
		}
		fid, exists := canonicalFolderID[parent]
		if !exists {
			return 0, fmt.Errorf("live parent folder %q is missing", parent)
		}
		return fid, nil
	}
	for _, folder := range folders {
		if folder.Action == "delete" {
			continue
		}
		fid, err := resolveParent(folder.Path)
		if err != nil {
			return err
		}
		level := safeLegacyFolderLevel(folder.Path)
		if folder.FID == fid && folder.Level == level {
			continue
		}
		if err := tx.Model(&model.Folder{}).Where("id = ? AND vault_id = ?", folder.ID, vaultID).
			Updates(map[string]any{"fid": fid, "level": level}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.Note{}) {
		var notes []model.Note
		if err := tx.Where("vault_id = ? AND action <> ?", vaultID, "delete").Find(&notes).Error; err != nil {
			return err
		}
		for _, note := range notes {
			fid, err := resolveParent(note.Path)
			if err != nil {
				return err
			}
			if note.FID != fid {
				if err := tx.Model(&model.Note{}).Where("id = ? AND vault_id = ?", note.ID, vaultID).UpdateColumn("fid", fid).Error; err != nil {
					return err
				}
			}
		}
	}
	if tx.Migrator().HasTable(&model.File{}) {
		var files []model.File
		if err := tx.Where("vault_id = ? AND action <> ?", vaultID, "delete").Find(&files).Error; err != nil {
			return err
		}
		for _, file := range files {
			fid, err := resolveParent(file.Path)
			if err != nil {
				return err
			}
			if file.FID != fid {
				if err := tx.Model(&model.File{}).Where("id = ? AND vault_id = ?", file.ID, vaultID).UpdateColumn("fid", fid).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateSafeMutationRequest(resourceType domain.SyncResourceType, request *dto.SafeMutationRequest) error {
	if request == nil || strings.TrimSpace(request.DeviceID) == "" || strings.TrimSpace(request.OperationID) == "" {
		return errors.New("deviceId and operationId are required")
	}
	if request.Path == "" || request.PathHash == "" {
		return errors.New("path and pathHash are required")
	}
	switch request.Action {
	case "CREATE":
		if request.ResourceID != "" || request.BaseRevision != 0 || request.ExpectedPathState != string(domain.ExpectedPathStateAbsent) {
			return newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "create requires absent path and revision zero")
		}
	case "MODIFY", "DELETE":
		if request.ResourceID == "" || request.BaseRevision <= 0 || request.ExpectedPathState != string(domain.ExpectedPathStatePresent) {
			return newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "existing mutation requires resource, revision, and present path")
		}
	case "RENAME":
		if request.ResourceID == "" || request.BaseRevision <= 0 || request.ExpectedPathState != string(domain.ExpectedPathStatePresent) || request.PreviousPath == "" || request.PreviousPathHash == "" {
			return newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "rename requires current and previous path state")
		}
	default:
		return errors.New("unsupported safe mutation action")
	}
	if resourceType != domain.SyncResourceTypeNote && resourceType != domain.SyncResourceTypeFile && resourceType != domain.SyncResourceTypeFolder {
		return errors.New("unsupported safe mutation resource type")
	}
	if resourceType == domain.SyncResourceTypeNote && (request.Action == "CREATE" || request.Action == "MODIFY") {
		content := []byte(request.Content)
		if int64(len(content)) != request.Size {
			return newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "note size does not match content")
		}
		if !matchesSafeContentHash(content, request.ContentHash) {
			return newSafeSyncError(domain.SafeSyncErrorPathStateConflict, "note hash does not match content")
		}
	}
	return nil
}

func requireStrictVaultTransaction(tx *gorm.DB, vaultID int64) error {
	var state model.VaultSyncState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("vault_id = ?", vaultID).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return newSafeSyncError(domain.SafeSyncErrorStrictRequired, "vault is not strict")
	}
	if err != nil {
		return err
	}
	switch state.State {
	case string(domain.VaultSyncStateStrict):
		return nil
	case string(domain.VaultSyncStateBootstrapping):
		return newSafeSyncError(domain.SafeSyncErrorBootstrapInProgress, "vault bootstrap is in progress")
	default:
		return newSafeSyncError(domain.SafeSyncErrorStrictRequired, "vault is not strict")
	}
}

func safeMutationFingerprint(vaultID int64, resourceType domain.SyncResourceType, request *dto.SafeMutationRequest) (string, error) {
	fingerprintRequest := *request
	fingerprintRequest.Context = ""
	payload, err := json.Marshal(struct {
		VaultID      int64
		ResourceType domain.SyncResourceType
		Request      *dto.SafeMutationRequest
	}{VaultID: vaultID, ResourceType: resourceType, Request: &fingerprintRequest})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func findSafeSyncOperation(tx *gorm.DB, vaultID int64, deviceID, operationID string) (*model.SyncOperation, error) {
	var operation model.SyncOperation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("vault_id = ? AND device_id = ? AND operation_id = ?", vaultID, deviceID, operationID).Take(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &operation, err
}

func replaySafeSyncOperation(operation *model.SyncOperation, fingerprint string, now time.Time) (*dto.SafeMutationResponse, error) {
	if operation.State == string(domain.SyncOperationStateRejected) {
		return nil, newSafeSyncError(domain.SafeSyncErrorCode(operation.ErrorCode), "operation was previously rejected")
	}
	if operation.RequestFingerprint != fingerprint {
		return nil, newSafeSyncError(domain.SafeSyncErrorOperationIDReused, "operationId is bound to a different request")
	}
	if operation.ExpiresAt.Before(now) && operation.State != string(domain.SyncOperationStatePrepared) {
		return nil, newSafeSyncError(domain.SafeSyncErrorOperationExpired, "operation result retention expired")
	}
	switch operation.State {
	case string(domain.SyncOperationStateCommitted):
		return &dto.SafeMutationResponse{
			ResourceID: operation.ResourceID, ResourceRevision: operation.ResourceRevision,
			VaultRevision: operation.VaultRevision, ContentHash: operation.ContentHash, Outcome: operation.Outcome,
			Replayed: true,
		}, nil
	default:
		return nil, newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "operation is still prepared")
	}
}

func createCommittedSafeSyncOperation(tx *gorm.DB, vaultID int64, request *dto.SafeMutationRequest, resourceType domain.SyncResourceType, fingerprint string, result *dto.SafeMutationResponse, now time.Time) error {
	return tx.Create(&model.SyncOperation{
		VaultID: vaultID, DeviceID: request.DeviceID, OperationID: request.OperationID,
		Action: string(resourceType) + ":" + request.Action, RequestFingerprint: fingerprint,
		State: string(domain.SyncOperationStateCommitted), ResourceID: result.ResourceID,
		ResourceRevision: result.ResourceRevision, VaultRevision: result.VaultRevision,
		ContentHash: result.ContentHash, Outcome: result.Outcome,
		ExpiresAt: now.Add(model.SafeSyncOperationRetention),
	}).Error
}

func createRejectedSafeSyncOperation(tx *gorm.DB, vaultID int64, request *dto.SafeMutationRequest, resourceType domain.SyncResourceType, fingerprint string, errorCode domain.SafeSyncErrorCode, now time.Time) error {
	return tx.Create(&model.SyncOperation{
		VaultID: vaultID, DeviceID: request.DeviceID, OperationID: request.OperationID,
		Action: string(resourceType) + ":" + request.Action, RequestFingerprint: fingerprint,
		State: string(domain.SyncOperationStateRejected), ResourceID: request.ResourceID,
		ErrorCode: string(errorCode), ExpiresAt: now.Add(model.SafeSyncOperationRetention),
	}).Error
}

func createSafePathTombstone(tx *gorm.DB, resource *model.SyncResourceMetadata, path string, vaultRevision int64, transactionID string) error {
	return tx.Create(&model.SyncPathTombstone{
		VaultID: resource.VaultID, Path: path, PathHash: util.EncodeHash32(path), ResourceID: resource.ResourceID,
		ResourceRevision: resource.ResourceRevision, VaultRevision: vaultRevision, TransactionID: transactionID,
	}).Error
}

func createSafeSyncEvent(tx *gorm.DB, resource *model.SyncResourceMetadata, action, previousPath string, vaultRevision int64, transactionID, operationID string) error {
	if action != "RENAME" {
		previousPath = ""
	}
	return tx.Create(&model.SyncEvent{
		VaultID: resource.VaultID, VaultRevision: vaultRevision, ResourceID: resource.ResourceID,
		ResourceRevision: resource.ResourceRevision, ResourceType: resource.ResourceType, Action: action,
		Path: resource.CurrentPath, PreviousPath: previousPath, ContentHash: resource.ContentHash,
		State: resource.State, TransactionID: transactionID, OperationID: operationID,
	}).Error
}

func replaceSafePathPrefix(path, oldPrefix, newPrefix string) string {
	if path == oldPrefix {
		return newPrefix
	}
	return newPrefix + strings.TrimPrefix(path, oldPrefix)
}

func safeMutationOutcome(action string) string {
	switch action {
	case "CREATE":
		return "CREATED"
	case "MODIFY":
		return "MODIFIED"
	case "DELETE":
		return "DELETED"
	case "RENAME":
		return "RENAMED"
	default:
		return action
	}
}
