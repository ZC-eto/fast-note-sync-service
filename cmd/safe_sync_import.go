package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	internalApp "github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type safeSyncUserImporter interface {
	ImportUser(ctx context.Context, uid int64, dryRun bool) (*dao.SafeSyncImportReport, error)
}

type safeSyncImportOptions struct {
	configPath string
	sourcePath string
	uids       []int64
	apply      bool
}

type safeSyncImportOutput struct {
	Mode  string                      `json:"mode"`
	Users []*dao.SafeSyncImportReport `json:"users"`
}

func newSafeSyncImportCommand() *cobra.Command {
	options := safeSyncImportOptions{}
	command := &cobra.Command{
		Use:   "safe-sync-import",
		Short: "Inventory or import legacy SQLite user data into PostgreSQL",
		Long: `Inventory or import legacy SQLite user databases into PostgreSQL.

The command is read-only by default. Pass --apply to import and persist VERIFIED
only after every source/target table check succeeds in one transaction.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeSafeSyncImport(cmd.Context(), cmd.OutOrStdout(), options)
		},
	}
	flags := command.Flags()
	flags.StringVarP(&options.configPath, "config", "c", "config/config.yaml", "config file path")
	flags.StringVar(&options.sourcePath, "source-path", "", "legacy SQLite base database path (defaults to database.path)")
	flags.Int64SliceVar(&options.uids, "uid", nil, "user UID to import; repeat or comma-separate, defaults to all active users")
	flags.BoolVar(&options.apply, "apply", false, "write and verify the PostgreSQL import")
	return command
}

func executeSafeSyncImport(ctx context.Context, output io.Writer, options safeSyncImportOptions) error {
	appConfig, _, err := internalApp.LoadConfig(options.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !strings.EqualFold(appConfig.Database.Type, "sqlite") {
		return fmt.Errorf("safe sync import requires database.type=sqlite for the user inventory source")
	}
	if !strings.EqualFold(appConfig.UserDatabase.Type, "postgres") {
		return fmt.Errorf("safe sync import requires user-database.type=postgres for the target")
	}

	mainConfig := appConfig.Database
	mainConfig.RunMode = appConfig.Server.RunMode
	if _, err := os.Stat(mainConfig.Path); err != nil {
		return fmt.Errorf("open SQLite main database inventory %q: %w", mainConfig.Path, err)
	}
	mainDB, err := dao.NewEngine(mainConfig, zap.NewNop())
	if err != nil {
		return fmt.Errorf("open SQLite main database inventory: %w", err)
	}
	if sqlDB, dbErr := mainDB.DB(); dbErr == nil {
		defer sqlDB.Close()
	}

	sourceConfig := mainConfig
	if options.sourcePath != "" {
		sourceConfig.Path = options.sourcePath
	}
	targetConfig := appConfig.UserDatabase
	targetConfig.RunMode = appConfig.Server.RunMode
	if err := pingSafeSyncPostgresTarget(ctx, targetConfig); err != nil {
		return err
	}

	source := dao.New(mainDB, ctx,
		dao.WithConfig(&mainConfig),
		dao.WithUserDatabaseConfig(&sourceConfig),
		dao.WithLogger(zap.NewNop()),
	)
	target := dao.New(mainDB, ctx,
		dao.WithConfig(&mainConfig),
		dao.WithUserDatabaseConfig(&targetConfig),
		dao.WithLogger(zap.NewNop()),
	)
	importer, err := dao.NewSafeSyncPostgresImporter(source, target)
	if err != nil {
		return err
	}

	uids := append([]int64(nil), options.uids...)
	if len(uids) == 0 {
		uids, err = listSafeSyncImportUIDs(ctx, mainDB)
		if err != nil {
			return fmt.Errorf("list active users: %w", err)
		}
	}
	if len(uids) == 0 {
		return fmt.Errorf("no active users found for safe sync import")
	}
	reports, err := runSafeSyncImports(ctx, importer, uids, options.apply)
	if err != nil {
		return err
	}
	mode := "dry-run"
	if options.apply {
		mode = "apply"
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(safeSyncImportOutput{Mode: mode, Users: reports}); err != nil {
		return fmt.Errorf("write import report: %w", err)
	}
	return nil
}

func pingSafeSyncPostgresTarget(ctx context.Context, targetConfig config.DatabaseConfig) error {
	targetDB, err := dao.NewEngine(targetConfig, zap.NewNop())
	if err != nil {
		return fmt.Errorf("connect PostgreSQL import target: %w", err)
	}
	sqlDB, err := targetDB.DB()
	if err != nil {
		return fmt.Errorf("get PostgreSQL import target connection: %w", err)
	}
	defer sqlDB.Close()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL import target: %w", err)
	}
	return nil
}

func runSafeSyncImports(ctx context.Context, importer safeSyncUserImporter, uids []int64, apply bool) ([]*dao.SafeSyncImportReport, error) {
	sortedUIDs := append([]int64(nil), uids...)
	sort.Slice(sortedUIDs, func(i, j int) bool { return sortedUIDs[i] < sortedUIDs[j] })
	reports := make([]*dao.SafeSyncImportReport, 0, len(sortedUIDs))
	for _, uid := range sortedUIDs {
		if uid <= 0 {
			return nil, fmt.Errorf("uid must be positive: %d", uid)
		}
		report, err := importer.ImportUser(ctx, uid, !apply)
		if err != nil {
			return nil, fmt.Errorf("safe sync import uid %d: %w", uid, err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func listSafeSyncImportUIDs(ctx context.Context, db *gorm.DB) ([]int64, error) {
	if db == nil {
		return nil, fmt.Errorf("main database is required")
	}
	var uids []int64
	err := db.WithContext(ctx).
		Model(&model.User{}).
		Where("is_deleted = ?", 0).
		Order("uid").
		Pluck("uid", &uids).Error
	if err != nil {
		return nil, err
	}
	return uids, nil
}

func init() {
	rootCmd.AddCommand(newSafeSyncImportCommand())
}
