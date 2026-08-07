package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	safeSyncBootstrapTTL    = 10 * time.Minute
	safeSyncDefaultPageSize = 200
	safeSyncMaximumPageSize = 500
)

type SafeSyncService interface {
	Status(ctx context.Context, uid, vaultID int64) (*dto.SafeSyncStatusResponse, error)
	BootstrapStart(ctx context.Context, uid, vaultID int64, deviceID string) (*dto.SafeSyncBootstrapStartResponse, error)
	BootstrapPage(ctx context.Context, uid, vaultID int64, sessionID, cursor string, pageSize int) (*dto.SafeSyncBootstrapPageResponse, error)
	BootstrapCommit(ctx context.Context, uid, vaultID int64, sessionID, manifestHash string, snapshotRevision int64) (*dto.SafeSyncStatusResponse, error)
	BootstrapCancel(ctx context.Context, uid, vaultID int64, sessionID string) (*dto.SafeSyncStatusResponse, error)
	Events(ctx context.Context, uid, vaultID, afterRevision int64, pageSize int) (*dto.SafeSyncEventsResponse, error)
}

type safeSyncService struct {
	uow              *dao.SafeSyncUnitOfWork
	userDatabaseType string
	cursorKey        []byte
	now              func() time.Time
}

type safeSyncCursor struct {
	UID          int64  `json:"u"`
	VaultID      int64  `json:"v"`
	SessionID    string `json:"s"`
	Offset       int    `json:"o"`
	ExpiresAt    int64  `json:"e"`
	ManifestHash string `json:"m"`
}

func NewSafeSyncService(uow *dao.SafeSyncUnitOfWork, userDatabaseType string, cursorKey []byte) SafeSyncService {
	key := append([]byte(nil), cursorKey...)
	return &safeSyncService{
		uow:              uow,
		userDatabaseType: strings.ToLower(strings.TrimSpace(userDatabaseType)),
		cursorKey:        key,
		now:              func() time.Time { return time.Now().UTC() },
	}
}

func (s *safeSyncService) Status(ctx context.Context, uid, vaultID int64) (*dto.SafeSyncStatusResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return unsupportedSafeSyncStatus(), err
	}

	now := s.now()
	var state model.VaultSyncState
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		current, err := ensureSafeSyncVaultState(tx, vaultID, now)
		if err != nil {
			return err
		}
		state = *current
		if state.State == string(domain.VaultSyncStateBootstrapping) && bootstrapExpired(&state, now) {
			if err := restoreBootstrapPreviousState(tx, &state); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return safeSyncStatusFromModel(&state, true, uid), nil
}

func (s *safeSyncService) BootstrapStart(ctx context.Context, uid, vaultID int64, deviceID string) (*dto.SafeSyncBootstrapStartResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return nil, err
	}
	if strings.TrimSpace(deviceID) == "" {
		return nil, newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "deviceId is required")
	}

	now := s.now()
	var state model.VaultSyncState
	createdSession := false
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		current, err := ensureSafeSyncVaultState(tx, vaultID, now)
		if err != nil {
			return err
		}
		if current.State == string(domain.VaultSyncStateBootstrapping) && bootstrapExpired(current, now) {
			if err := restoreBootstrapPreviousState(tx, current); err != nil {
				return err
			}
		}
		if current.State == string(domain.VaultSyncStateBootstrapping) {
			if current.BootstrapDeviceID != deviceID || current.BootstrapSessionID == "" {
				return newSafeSyncError(domain.SafeSyncErrorBootstrapInProgress, "another device owns the bootstrap session")
			}
			state = *current
			return nil
		}

		previousState := current.State
		if previousState != string(domain.VaultSyncStateStrict) {
			previousState = string(domain.VaultSyncStateOff)
		}
		expiresAt := now.Add(safeSyncBootstrapTTL)
		updates := map[string]any{
			"state":                       string(domain.VaultSyncStateBootstrapping),
			"bootstrap_session_id":        uuid.NewString(),
			"bootstrap_device_id":         deviceID,
			"bootstrap_previous_state":    previousState,
			"bootstrap_expires_at":        expiresAt,
			"bootstrap_manifest_hash":     "",
			"bootstrap_snapshot_revision": current.LatestVaultRevision,
		}
		if err := tx.Model(&model.VaultSyncState{}).Where("vault_id = ? AND state = ?", vaultID, current.State).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Where("vault_id = ?", vaultID).Take(current).Error; err != nil {
			return err
		}
		createdSession = true
		state = *current
		return nil
	})
	if err != nil {
		return nil, err
	}

	manifestHash, resourceCount, err := s.bootstrapManifest(ctx, uid, vaultID)
	if err != nil {
		if createdSession {
			_, _ = s.BootstrapCancel(ctx, uid, vaultID, state.BootstrapSessionID)
		}
		return nil, err
	}
	if state.BootstrapManifestHash == "" {
		err = s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
			result := tx.Model(&model.VaultSyncState{}).
				Where("vault_id = ? AND state = ? AND bootstrap_session_id = ? AND bootstrap_manifest_hash = ''",
					vaultID, string(domain.VaultSyncStateBootstrapping), state.BootstrapSessionID).
				Update("bootstrap_manifest_hash", manifestHash)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "bootstrap session changed while preparing manifest")
			}
			state.BootstrapManifestHash = manifestHash
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if state.BootstrapManifestHash != manifestHash {
		return nil, newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "bootstrap manifest changed")
	}

	cursor, err := s.encodeCursor(safeSyncCursor{
		UID: uid, VaultID: vaultID, SessionID: state.BootstrapSessionID, Offset: 0,
		ExpiresAt: state.BootstrapExpiresAt.UnixMilli(), ManifestHash: manifestHash,
	})
	if err != nil {
		return nil, err
	}
	return &dto.SafeSyncBootstrapStartResponse{
		State:                 state.State,
		SessionID:             state.BootstrapSessionID,
		ExpiresAt:             state.BootstrapExpiresAt.UnixMilli(),
		SnapshotVaultRevision: state.BootstrapSnapshotRevision,
		ManifestHash:          manifestHash,
		ResourceCount:         resourceCount,
		Cursor:                cursor,
	}, nil
}

func (s *safeSyncService) BootstrapPage(ctx context.Context, uid, vaultID int64, sessionID, cursor string, pageSize int) (*dto.SafeSyncBootstrapPageResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return nil, err
	}
	decoded, err := s.decodeCursor(cursor)
	if err != nil || decoded.UID != uid || decoded.VaultID != vaultID || decoded.SessionID != sessionID {
		return nil, newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "invalid bootstrap cursor")
	}
	if decoded.ExpiresAt <= s.now().UnixMilli() {
		return nil, newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "bootstrap cursor expired")
	}
	pageSize = normalizeSafeSyncPageSize(pageSize)

	var state model.VaultSyncState
	items := make([]dto.SafeSyncManifestItem, 0, pageSize)
	var total int64
	err = s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := tx.Where("vault_id = ?", vaultID).Take(&state).Error; err != nil {
			return err
		}
		if state.State != string(domain.VaultSyncStateBootstrapping) || state.BootstrapSessionID != sessionID ||
			state.BootstrapManifestHash == "" || state.BootstrapManifestHash != decoded.ManifestHash || bootstrapExpired(&state, s.now()) {
			return newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "bootstrap session is no longer valid")
		}
		if err := tx.Model(&model.SyncResourceMetadata{}).Where("vault_id = ?", vaultID).Count(&total).Error; err != nil {
			return err
		}
		var resources []model.SyncResourceMetadata
		if err := tx.Where("vault_id = ?", vaultID).Order("resource_id").Offset(decoded.Offset).Limit(pageSize).Find(&resources).Error; err != nil {
			return err
		}
		for _, resource := range resources {
			items = append(items, safeSyncManifestItemFromModel(resource))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	nextOffset := decoded.Offset + len(items)
	nextCursor := ""
	if int64(nextOffset) < total {
		nextCursor, err = s.encodeCursor(safeSyncCursor{
			UID: uid, VaultID: vaultID, SessionID: sessionID, Offset: nextOffset,
			ExpiresAt: decoded.ExpiresAt, ManifestHash: decoded.ManifestHash,
		})
		if err != nil {
			return nil, err
		}
	}
	return &dto.SafeSyncBootstrapPageResponse{
		SessionID:             sessionID,
		SnapshotVaultRevision: state.BootstrapSnapshotRevision,
		ManifestHash:          state.BootstrapManifestHash,
		Items:                 items,
		NextCursor:            nextCursor,
	}, nil
}

func (s *safeSyncService) BootstrapCommit(ctx context.Context, uid, vaultID int64, sessionID, manifestHash string, snapshotRevision int64) (*dto.SafeSyncStatusResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return nil, err
	}
	now := s.now()
	var state model.VaultSyncState
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("vault_id = ?", vaultID).Take(&state).Error; err != nil {
			return err
		}
		if state.State != string(domain.VaultSyncStateBootstrapping) || state.BootstrapSessionID != sessionID || bootstrapExpired(&state, now) ||
			state.BootstrapManifestHash == "" || state.BootstrapManifestHash != manifestHash || state.BootstrapSnapshotRevision != snapshotRevision {
			return newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "bootstrap commit compare-and-swap failed")
		}
		updates := clearedBootstrapUpdates(string(domain.VaultSyncStateStrict))
		updates["activated_at"] = now
		if err := tx.Model(&model.VaultSyncState{}).Where("vault_id = ? AND bootstrap_session_id = ?", vaultID, sessionID).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("vault_id = ?", vaultID).Take(&state).Error
	})
	if err != nil {
		return nil, err
	}
	return safeSyncStatusFromModel(&state, true, uid), nil
}

func (s *safeSyncService) BootstrapCancel(ctx context.Context, uid, vaultID int64, sessionID string) (*dto.SafeSyncStatusResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return nil, err
	}
	var state model.VaultSyncState
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("vault_id = ?", vaultID).Take(&state).Error; err != nil {
			return err
		}
		if state.State == string(domain.VaultSyncStateStrict) {
			return newSafeSyncError(domain.SafeSyncErrorStrictRequired, "strict vault cannot be downgraded by bootstrap cancel")
		}
		if state.State != string(domain.VaultSyncStateBootstrapping) || state.BootstrapSessionID != sessionID {
			return newSafeSyncError(domain.SafeSyncErrorBootstrapStateConflict, "bootstrap session does not match")
		}
		if err := restoreBootstrapPreviousState(tx, &state); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return safeSyncStatusFromModel(&state, true, uid), nil
}

func (s *safeSyncService) Events(ctx context.Context, uid, vaultID, afterRevision int64, pageSize int) (*dto.SafeSyncEventsResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return nil, err
	}
	pageSize = normalizeSafeSyncPageSize(pageSize)
	var state model.VaultSyncState
	var events []model.SyncEvent
	var minimumRevision int64
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if err := tx.Where("vault_id = ?", vaultID).Take(&state).Error; err != nil {
			return err
		}
		switch state.State {
		case string(domain.VaultSyncStateBootstrapping):
			return newSafeSyncError(domain.SafeSyncErrorBootstrapInProgress, "vault bootstrap is in progress")
		case string(domain.VaultSyncStateStrict):
		default:
			return newSafeSyncError(domain.SafeSyncErrorStrictRequired, "vault is not strict")
		}
		if err := tx.Model(&model.SyncEvent{}).Where("vault_id = ?", vaultID).
			Select("COALESCE(MIN(vault_revision), 0)").Scan(&minimumRevision).Error; err != nil {
			return err
		}
		if minimumRevision > 0 && afterRevision < minimumRevision-1 {
			return newSafeSyncError(domain.SafeSyncErrorRebootstrapRequired, "event cursor is older than the retained event window")
		}
		return tx.Where("vault_id = ? AND vault_revision > ?", vaultID, afterRevision).
			Order("vault_revision").Limit(pageSize + 1).Find(&events).Error
	})
	if err != nil {
		return nil, err
	}

	hasMore := len(events) > pageSize
	if hasMore {
		events = events[:pageSize]
	}
	result := &dto.SafeSyncEventsResponse{
		Events:              make([]dto.SafeSyncEvent, 0, len(events)),
		LatestVaultRevision: state.LatestVaultRevision,
		NextRevision:        afterRevision,
		HasMore:             hasMore,
	}
	for _, event := range events {
		result.Events = append(result.Events, safeSyncEventFromModel(event))
		result.NextRevision = event.VaultRevision
	}
	return result, nil
}

func (s *safeSyncService) requireCapability(ctx context.Context, uid int64) error {
	if s == nil || s.uow == nil || s.userDatabaseType != "postgres" || len(s.cursorKey) == 0 {
		return newSafeSyncError(domain.SafeSyncErrorUnsupported, "safe revision sync requires a verified PostgreSQL user database")
	}
	verified, err := s.uow.IsMigrationVerified(ctx, uid)
	if err != nil {
		return err
	}
	if !verified {
		return newSafeSyncError(domain.SafeSyncErrorUnsupported, "PostgreSQL user data import is not verified")
	}
	return nil
}

func (s *safeSyncService) bootstrapManifest(ctx context.Context, uid, vaultID int64) (string, int64, error) {
	var resources []model.SyncResourceMetadata
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		return tx.Where("vault_id = ?", vaultID).Order("resource_id").Find(&resources).Error
	})
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	for _, resource := range resources {
		encoded, err := json.Marshal(safeSyncManifestItemFromModel(resource))
		if err != nil {
			return "", 0, err
		}
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{0x1e})
	}
	return hex.EncodeToString(hash.Sum(nil)), int64(len(resources)), nil
}

func (s *safeSyncService) encodeCursor(cursor safeSyncCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := hmac.New(sha256.New, s.cursorKey)
	_, _ = signature.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

func (s *safeSyncService) decodeCursor(cursor string) (safeSyncCursor, error) {
	var decoded safeSyncCursor
	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		return decoded, errors.New("invalid safe sync cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return decoded, err
	}
	expected := hmac.New(sha256.New, s.cursorKey)
	_, _ = expected.Write([]byte(parts[0]))
	if !hmac.Equal(signature, expected.Sum(nil)) {
		return decoded, errors.New("invalid safe sync cursor signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return decoded, err
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return decoded, err
	}
	if decoded.Offset < 0 || decoded.SessionID == "" || decoded.ManifestHash == "" {
		return safeSyncCursor{}, errors.New("invalid safe sync cursor payload")
	}
	return decoded, nil
}

func ensureSafeSyncVaultState(tx *gorm.DB, vaultID int64, now time.Time) (*model.VaultSyncState, error) {
	var state model.VaultSyncState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("vault_id = ?", vaultID).Take(&state).Error
	if err == nil {
		return &state, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	state = model.VaultSyncState{
		VaultID:             vaultID,
		State:               string(domain.VaultSyncStateOff),
		MigrationVerifiedAt: &now,
	}
	if err := tx.Create(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func restoreBootstrapPreviousState(tx *gorm.DB, state *model.VaultSyncState) error {
	restoreState := state.BootstrapPreviousState
	if restoreState != string(domain.VaultSyncStateStrict) {
		restoreState = string(domain.VaultSyncStateOff)
	}
	if err := tx.Model(&model.VaultSyncState{}).Where("vault_id = ?", state.VaultID).Updates(clearedBootstrapUpdates(restoreState)).Error; err != nil {
		return err
	}
	return tx.Where("vault_id = ?", state.VaultID).Take(state).Error
}

func clearedBootstrapUpdates(state string) map[string]any {
	return map[string]any{
		"state":                       state,
		"bootstrap_session_id":        "",
		"bootstrap_device_id":         "",
		"bootstrap_previous_state":    "",
		"bootstrap_expires_at":        nil,
		"bootstrap_manifest_hash":     "",
		"bootstrap_snapshot_revision": 0,
	}
}

func bootstrapExpired(state *model.VaultSyncState, now time.Time) bool {
	return state.BootstrapExpiresAt == nil || !state.BootstrapExpiresAt.After(now)
}

func normalizeSafeSyncPageSize(pageSize int) int {
	if pageSize <= 0 {
		return safeSyncDefaultPageSize
	}
	if pageSize > safeSyncMaximumPageSize {
		return safeSyncMaximumPageSize
	}
	return pageSize
}

func unsupportedSafeSyncStatus() *dto.SafeSyncStatusResponse {
	return &dto.SafeSyncStatusResponse{Capability: false, State: string(domain.VaultSyncStateOff)}
}

func safeSyncStatusFromModel(state *model.VaultSyncState, verified bool, uid int64) *dto.SafeSyncStatusResponse {
	response := &dto.SafeSyncStatusResponse{
		Capability:          true,
		State:               state.State,
		LatestVaultRevision: state.LatestVaultRevision,
		MigrationVerified:   verified,
		BootstrapSessionID:  state.BootstrapSessionID,
		UID:                 uid,
		VaultID:             state.VaultID,
	}
	if state.BootstrapExpiresAt != nil {
		response.BootstrapExpiresAt = state.BootstrapExpiresAt.UnixMilli()
	}
	return response
}

func safeSyncManifestItemFromModel(resource model.SyncResourceMetadata) dto.SafeSyncManifestItem {
	return dto.SafeSyncManifestItem{
		ResourceID:       resource.ResourceID,
		ResourceType:     resource.ResourceType,
		Path:             resource.CurrentPath,
		PathHash:         resource.CurrentPathHash,
		State:            resource.State,
		ResourceRevision: resource.ResourceRevision,
		ContentHash:      resource.ContentHash,
		Size:             resource.Size,
	}
}

func safeSyncEventFromModel(event model.SyncEvent) dto.SafeSyncEvent {
	return dto.SafeSyncEvent{
		VaultRevision:    event.VaultRevision,
		ResourceID:       event.ResourceID,
		ResourceRevision: event.ResourceRevision,
		ResourceType:     event.ResourceType,
		Action:           event.Action,
		Path:             event.Path,
		PreviousPath:     event.PreviousPath,
		ContentHash:      event.ContentHash,
		State:            event.State,
		TransactionID:    event.TransactionID,
		OperationID:      event.OperationID,
	}
}

func newSafeSyncError(errorCode domain.SafeSyncErrorCode, message string) error {
	return &domain.SafeSyncError{Code: errorCode, Message: message}
}

func safeSyncErrorCode(err error) domain.SafeSyncErrorCode {
	var safeErr *domain.SafeSyncError
	if errors.As(err, &safeErr) {
		return safeErr.Code
	}
	return ""
}

var _ SafeSyncService = (*safeSyncService)(nil)
