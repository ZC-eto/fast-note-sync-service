package dao

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSafeSyncPostgresIntegration_ImportRetrySequenceAndRevisionAllocation(t *testing.T) {
	dsn := os.Getenv("SAFE_SYNC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SAFE_SYNC_TEST_POSTGRES_DSN is not set")
	}

	postgresConfig := parseSafeSyncPostgresTestConfig(t, dsn)
	rootDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	rootSQLDB, err := rootDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rootSQLDB.Close() })

	uid := randomSafeSyncTestUID(t)
	schemaName := fmt.Sprintf("user_%d", uid)
	t.Cleanup(func() {
		require.NoError(t, rootDB.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schemaName)).Error)
	})

	ctx := context.Background()
	source := setupSafeSyncImportDao(t, "postgres-integration-source")
	sourceVaultDB := source.ResolveDB(fmt.Sprintf("user_vault_%d", uid))
	require.NoError(t, sourceVaultDB.AutoMigrate(&model.Vault{}))
	require.NoError(t, sourceVaultDB.Create(&model.Vault{ID: 3, Vault: "Personal", NoteCount: 1}).Error)
	sourceNoteDB := source.ResolveDB(fmt.Sprintf("user_%d", uid))
	require.NoError(t, sourceNoteDB.AutoMigrate(&model.Note{}))
	require.NoError(t, sourceNoteDB.Create(&model.Note{
		ID: 11, VaultID: 3, Action: "modify", Path: "a.md", PathHash: "path-a", ContentHash: "hash-a", Size: 12,
	}).Error)

	mainDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	target := New(mainDB, ctx,
		WithConfig(postgresConfig),
		WithUserDatabaseConfig(postgresConfig),
		WithLogger(zap.NewNop()),
	)
	t.Cleanup(func() {
		for _, entry := range target.KeyDb {
			if sqlDB, closeErr := entry.db.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
		}
		if sqlDB, closeErr := mainDB.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	importer, err := NewSafeSyncPostgresImporter(source, target)
	require.NoError(t, err)
	dryRun, err := importer.ImportUser(ctx, uid, true)
	require.NoError(t, err)
	require.False(t, dryRun.Verified)
	require.Equal(t, int64(1), dryRun.Table(model.TableNameNote).SourceCount)

	for range 2 {
		report, importErr := importer.ImportUser(ctx, uid, false)
		require.NoError(t, importErr)
		require.True(t, report.Verified)
		require.Equal(t, report.Table(model.TableNameNote).SourceKeyDigest, report.Table(model.TableNameNote).TargetKeyDigest)
	}

	targetDB := target.ResolveDB(NewSafeSyncUnitOfWork(target).GetKey(uid))
	verified, err := NewSafeSyncUnitOfWork(target).IsMigrationVerified(ctx, uid)
	require.NoError(t, err)
	require.True(t, verified)
	var imported model.Note
	require.NoError(t, targetDB.First(&imported, 11).Error)
	newNote := model.Note{VaultID: 3, Action: "create", Path: "b.md", PathHash: "path-b", ContentHash: "hash-b"}
	require.NoError(t, targetDB.Create(&newNote).Error)
	require.Greater(t, newNote.ID, int64(11), "PostgreSQL sequence must advance beyond imported primary keys")

	uow := NewSafeSyncUnitOfWork(target)
	const workers = 8
	revisions := make(chan int64, workers)
	errorsByWorker := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			txErr := uow.Transaction(ctx, uid, func(tx *gorm.DB) error {
				_, end, allocateErr := uow.AllocateVaultRevisions(tx, 3, 1)
				if allocateErr == nil {
					revisions <- end
				}
				return allocateErr
			})
			errorsByWorker <- txErr
		}()
	}
	wg.Wait()
	close(revisions)
	close(errorsByWorker)
	for workerErr := range errorsByWorker {
		require.NoError(t, workerErr)
	}
	got := make([]int, 0, workers)
	for revision := range revisions {
		got = append(got, int(revision))
	}
	sort.Ints(got)
	require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, got)
}

func parseSafeSyncPostgresTestConfig(t *testing.T, dsn string) *config.DatabaseConfig {
	t.Helper()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	password, _ := parsed.User.Password()
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		require.NoError(t, err)
	}
	sslMode := parsed.Query().Get("sslmode")
	if sslMode == "" {
		sslMode = "disable"
	}
	return &config.DatabaseConfig{
		Type:             "postgres",
		Host:             parsed.Hostname(),
		Port:             port,
		UserName:         parsed.User.Username(),
		Password:         password,
		Name:             strings.TrimPrefix(parsed.Path, "/"),
		SSLMode:          sslMode,
		EnableWriteQueue: util.Ptr(false),
	}
}

func randomSafeSyncTestUID(t *testing.T) int64 {
	t.Helper()
	var value [4]byte
	_, err := rand.Read(value[:])
	require.NoError(t, err)
	return int64(binary.BigEndian.Uint32(value[:])%900_000_000 + 100_000_000)
}
