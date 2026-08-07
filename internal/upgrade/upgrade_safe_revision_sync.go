package upgrade

import (
	"context"
	"errors"
	"fmt"

	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"gorm.io/gorm"
)

type SafeRevisionSyncMigrate struct{}

func (*SafeRevisionSyncMigrate) Version() string {
	return "3.6.1"
}

func (*SafeRevisionSyncMigrate) Description() string {
	return "Create and backfill PostgreSQL safe revision sync metadata"
}

func (*SafeRevisionSyncMigrate) Up(_ *gorm.DB, ctx context.Context, mc *MigrationContext) error {
	if mc == nil || mc.Dao == nil {
		return errors.New("dao is nil in safe revision sync migration context")
	}
	if mc.UserDatabaseType != "postgres" {
		return nil
	}

	uids, err := mc.Dao.GetAllUserUIDs()
	if err != nil {
		return fmt.Errorf("list users for safe revision sync migration: %w", err)
	}
	uow := dao.NewSafeSyncUnitOfWork(mc.Dao)
	for _, uid := range uids {
		if err := uow.Migrate(ctx, uid); err != nil {
			return fmt.Errorf("migrate safe revision sync schema for uid %d: %w", uid, err)
		}
		if err := uow.BackfillResourceMetadata(ctx, uid); err != nil {
			return fmt.Errorf("backfill safe revision sync resources for uid %d: %w", uid, err)
		}
	}
	return nil
}
