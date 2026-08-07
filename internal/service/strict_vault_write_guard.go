package service

import (
	"context"
	"errors"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"gorm.io/gorm"
)

type StrictVaultWriteGuard struct {
	uow              *dao.SafeSyncUnitOfWork
	userDatabaseType string
}

func NewStrictVaultWriteGuard(uow *dao.SafeSyncUnitOfWork, userDatabaseType string) *StrictVaultWriteGuard {
	return &StrictVaultWriteGuard{uow: uow, userDatabaseType: strings.ToLower(strings.TrimSpace(userDatabaseType))}
}

func (g *StrictVaultWriteGuard) CheckLegacyWrite(ctx context.Context, uid, vaultID int64) error {
	state, supported, err := g.state(ctx, uid, vaultID)
	if err != nil || !supported || state == "" || state == string(domain.VaultSyncStateOff) {
		return err
	}
	switch state {
	case string(domain.VaultSyncStateBootstrapping):
		return code.ErrorSafeSyncBootstrapProgress.WithDetails("vault bootstrap is in progress")
	case string(domain.VaultSyncStateStrict):
		return code.ErrorSafeSyncStrictRequired.WithDetails("strict vault requires the safe revision sync protocol")
	default:
		return code.ErrorSafeSyncStrictRequired.WithDetails("unknown vault sync state: " + state)
	}
}

func (g *StrictVaultWriteGuard) CheckSafeWrite(ctx context.Context, uid, vaultID int64) error {
	state, supported, err := g.state(ctx, uid, vaultID)
	if err != nil {
		return err
	}
	if !supported {
		return newSafeSyncError(domain.SafeSyncErrorUnsupported, "safe revision sync requires a verified PostgreSQL user database")
	}
	switch state {
	case string(domain.VaultSyncStateStrict):
		return nil
	case string(domain.VaultSyncStateBootstrapping):
		return newSafeSyncError(domain.SafeSyncErrorBootstrapInProgress, "vault bootstrap is in progress")
	default:
		return newSafeSyncError(domain.SafeSyncErrorStrictRequired, "vault is not strict")
	}
}

func (g *StrictVaultWriteGuard) state(ctx context.Context, uid, vaultID int64) (string, bool, error) {
	if g == nil || g.uow == nil || g.userDatabaseType != "postgres" {
		return "", false, nil
	}
	verified, err := g.uow.IsMigrationVerified(ctx, uid)
	if err != nil || !verified {
		return "", false, err
	}
	var state model.VaultSyncState
	err = g.uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
		return tx.Select("state").Where("vault_id = ?", vaultID).Take(&state).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", true, nil
	}
	if err != nil {
		return "", true, err
	}
	return state.State, true, nil
}
