package app

import (
	"context"
	"fmt"

	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"go.uber.org/zap"
)

// Services encapsulates all business service instances
type Services struct {
	SafeSyncService         service.SafeSyncService
	SafeMutationCoordinator *service.SafeMutationCoordinator
	VaultService            service.VaultService
	NoteService             service.NoteService
	UserService             service.UserService
	TokenService            service.TokenService
	FileService             service.FileService
	SettingService          service.SettingService
	NoteHistoryService      service.NoteHistoryService
	ConflictService         service.ConflictService
	ShareService            service.ShareService
	NoteLinkService         service.NoteLinkService
	FolderService           service.FolderService
	StorageService          service.StorageService
	BackupService           service.BackupService
	GitSyncService          service.GitSyncService
	CloudflareService       service.CloudflareService
	SyncLogService          service.SyncLogService
	OIDCService             service.OIDCService
}

// initServices initializes all services
func initServices(cfg *AppConfig, infra *Infra, repos *Repositories, logger *zap.Logger) *Services {
	svcConfig := &service.ServiceConfig{
		User: service.UserServiceConfig{
			RegisterIsEnable: cfg.User.RegisterIsEnable,
			AdminUID:         cfg.User.AdminUID,
		},
		Token: service.TokenServiceConfig{
			WebGUILoginTokenExpiry: cfg.Security.WebGUILoginTokenExpiry,
			WebGUILoginTokenBindIP: *cfg.Security.WebGUILoginTokenBindIP,
		},
		App: service.AppServiceConfig{
			SoftDeleteRetentionTime: cfg.App.SoftDeleteRetentionTime,
			HistoryKeepVersions:     cfg.App.HistoryKeepVersions,
			HistorySaveDelay:        cfg.App.HistorySaveDelay,
			ShareTokenExpiry:        cfg.Security.ShareTokenExpiry,
			ShortLink: service.ShortLinkServiceConfig{
				BaseURL:  cfg.ShortLink.BaseURL,
				APIKey:   cfg.ShortLink.APIKey,
				Password: cfg.ShortLink.Password,
				Cloaking: cfg.ShortLink.Cloaking,
			},
		},
	}

	s := &Services{}
	safeSyncUOW := dao.NewSafeSyncUnitOfWork(infra.Dao)
	strictGuard := service.NewStrictVaultWriteGuard(safeSyncUOW, cfg.UserDatabase.Type)
	s.SafeSyncService = service.NewSafeSyncService(
		safeSyncUOW,
		cfg.UserDatabase.Type,
		[]byte(cfg.Security.AuthTokenKey),
	)
	s.SafeMutationCoordinator = service.NewSafeMutationCoordinator(safeSyncUOW, strictGuard)
	s.VaultService = service.NewVaultService(
		repos.VaultRepo,
		repos.NoteRepo,
		repos.FileRepo,
		repos.FolderRepo,
		repos.SyncLogRepo,
		repos.NoteHistoryRepo,
		repos.NoteLinkRepo,
		repos.SettingRepo,
		repos.NoteFTSRepo,
		repos.ShareRepo,
		repos.GitSyncRepo,
		repos.BackupRepo,
		logger,
		strictGuard,
	)
	s.StorageService = service.NewStorageService(repos.StorageRepo, &cfg.Storage)
	s.BackupService = service.NewBackupService(repos.BackupRepo, repos.NoteRepo, repos.FolderRepo, repos.FileRepo, repos.VaultRepo, s.StorageService, &cfg.Storage, cfg.App.TempPath, logger)
	s.GitSyncService = service.NewGitSyncService(repos.GitSyncRepo, repos.NoteRepo, repos.FolderRepo, repos.FileRepo, repos.VaultRepo, repos.SettingRepo, &cfg.Git, logger)

	// Initialize SyncLogService first, as NoteService/FileService/SettingService depend on it
	// SyncLogService 必须最先初始化，因为其他服务依赖它
	s.SyncLogService = service.NewSyncLogService(repos.SyncLogRepo, logger)

	s.FolderService = service.NewFolderService(repos.FolderRepo, repos.NoteRepo, repos.FileRepo, s.VaultService, s.BackupService, s.GitSyncService, s.SyncLogService, infra.workerPool, strictGuard)
	s.NoteService = service.NewNoteService(repos.UserRepo, repos.NoteRepo, repos.NoteLinkRepo, repos.FileRepo, repos.ShareRepo, s.VaultService, s.FolderService, s.BackupService, s.GitSyncService, s.SyncLogService, svcConfig, strictGuard)
	s.TokenService = service.NewTokenService(repos.AuthTokenRepo, repos.AuthTokenLogRepo, infra.TokenManager, logger, svcConfig.Token)
	s.UserService = service.NewUserService(repos.UserRepo, infra.TokenManager, s.TokenService, logger, svcConfig)
	s.OIDCService = service.NewOIDCService(repos.UserRepo, repos.OIDCIdentityRepo, s.TokenService)
	s.FileService = service.NewFileService(repos.UserRepo, repos.FileRepo, repos.NoteRepo, s.VaultService, s.FolderService, s.BackupService, s.GitSyncService, s.SyncLogService, svcConfig, strictGuard)
	s.SettingService = service.NewSettingService(repos.SettingRepo, s.VaultService, s.SyncLogService, svcConfig)
	s.NoteHistoryService = service.NewNoteHistoryService(repos.NoteHistoryRepo, repos.NoteRepo, repos.UserRepo, s.VaultService, s.FolderService, s.NoteService, s.BackupService, s.GitSyncService, logger, &svcConfig.App, strictGuard)
	s.ConflictService = service.NewConflictService(repos.NoteRepo, s.VaultService, logger, strictGuard)
	s.ShareService = service.NewShareService(repos.ShareRepo, infra.TokenManager, repos.NoteRepo, repos.FileRepo, repos.VaultRepo, logger, svcConfig)
	s.NoteLinkService = service.NewNoteLinkService(repos.NoteLinkRepo, repos.NoteRepo, s.VaultService)
	s.CloudflareService = service.NewCloudflareService(logger)

	return s
}

func recoverPreparedSafeSyncOperations(ctx context.Context, userRepo domain.UserRepository, recoverUser func(context.Context, int64) error) error {
	uids, err := userRepo.GetAllUIDs(ctx)
	if err != nil {
		return fmt.Errorf("list users for safe sync recovery: %w", err)
	}
	for _, uid := range uids {
		if err := recoverUser(ctx, uid); err != nil {
			return fmt.Errorf("recover safe sync operations for user %d: %w", uid, err)
		}
	}
	return nil
}
