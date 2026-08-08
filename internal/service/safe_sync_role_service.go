package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const devicePublisherLeaseTTL = 2 * time.Minute

func (s *safeSyncService) RegisterDeviceRole(ctx context.Context, uid, vaultID int64, deviceID string, role domain.DeviceSyncRole) (*dto.DeviceRoleStatusResponse, error) {
	if err := s.requireCapability(ctx, uid); err != nil {
		return nil, err
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || !validDeviceSyncRole(role) {
		return nil, newSafeSyncError(domain.SafeSyncErrorDeviceRoleConflict, "valid deviceId and role are required")
	}

	now := s.now()
	var response dto.DeviceRoleStatusResponse
	err := s.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		if _, err := ensureSafeSyncVaultState(tx, vaultID, now); err != nil {
			return err
		}
		var lockedVault model.VaultSyncState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("vault_id = ?", vaultID).Take(&lockedVault).Error; err != nil {
			return err
		}
		var publisher model.DeviceSyncRole
		publisherErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("vault_id = ? AND role = ? AND lease_expires_at > ?", vaultID, string(domain.DeviceSyncRoleLocalPublisher), now).
			Order("lease_expires_at DESC").Take(&publisher).Error
		if publisherErr != nil && !errors.Is(publisherErr, gorm.ErrRecordNotFound) {
			return publisherErr
		}
		if role == domain.DeviceSyncRoleLocalPublisher && publisher.ID != 0 && publisher.DeviceID != deviceID {
			return newSafeSyncError(domain.SafeSyncErrorDeviceRoleConflict, "another device owns the local publisher lease")
		}

		var leaseExpiresAt *time.Time
		if role == domain.DeviceSyncRoleLocalPublisher {
			expires := now.Add(devicePublisherLeaseTTL)
			leaseExpiresAt = &expires
			publisher = model.DeviceSyncRole{DeviceID: deviceID, LeaseExpiresAt: leaseExpiresAt}
		}
		entry := model.DeviceSyncRole{VaultID: vaultID, DeviceID: deviceID}
		updates := map[string]any{"role": string(role), "lease_expires_at": leaseExpiresAt, "last_seen_at": now}
		if err := tx.Where("vault_id = ? AND device_id = ?", vaultID, deviceID).
			Assign(updates).FirstOrCreate(&entry).Error; err != nil {
			return err
		}
		if publisher.DeviceID == deviceID && role != domain.DeviceSyncRoleLocalPublisher {
			publisher = model.DeviceSyncRole{}
		}
		response = dto.DeviceRoleStatusResponse{
			DeviceID: deviceID,
			Role:     string(role),
			Writable: role != domain.DeviceSyncRoleRemoteMirror && (publisher.DeviceID == "" || publisher.DeviceID == deviceID),
		}
		if publisher.DeviceID != "" {
			response.PublisherDeviceID = publisher.DeviceID
			if publisher.LeaseExpiresAt != nil {
				response.PublisherLeaseExpiresAt = publisher.LeaseExpiresAt.UnixMilli()
			}
		}
		return nil
	})
	return &response, err
}

func validDeviceSyncRole(role domain.DeviceSyncRole) bool {
	switch role {
	case domain.DeviceSyncRoleBidirectional, domain.DeviceSyncRoleLocalPublisher, domain.DeviceSyncRoleRemoteMirror:
		return true
	default:
		return false
	}
}

func enforceDeviceWriteRole(tx *gorm.DB, vaultID int64, deviceID string, now time.Time) error {
	var own model.DeviceSyncRole
	err := tx.Where("vault_id = ? AND device_id = ?", vaultID, deviceID).Take(&own).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if own.Role == string(domain.DeviceSyncRoleRemoteMirror) {
		return newSafeSyncError(domain.SafeSyncErrorDeviceReadOnly, "remote mirror devices cannot write")
	}
	var publisher model.DeviceSyncRole
	err = tx.Where("vault_id = ? AND role = ? AND lease_expires_at > ? AND device_id <> ?",
		vaultID, string(domain.DeviceSyncRoleLocalPublisher), now, deviceID).Take(&publisher).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return newSafeSyncError(domain.SafeSyncErrorDeviceRoleConflict, "the local publisher lease blocks writes from this device")
}
