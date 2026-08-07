package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeSafeSyncUserImporter struct {
	calls  []safeSyncImportCall
	failAt int64
}

type safeSyncImportCall struct {
	uid    int64
	dryRun bool
}

func (f *fakeSafeSyncUserImporter) ImportUser(_ context.Context, uid int64, dryRun bool) (*dao.SafeSyncImportReport, error) {
	f.calls = append(f.calls, safeSyncImportCall{uid: uid, dryRun: dryRun})
	if uid == f.failAt {
		return nil, errors.New("import failed")
	}
	return &dao.SafeSyncImportReport{UID: uid, DryRun: dryRun, Verified: !dryRun}, nil
}

func TestRunSafeSyncImportsDefaultsToDryRunAndSortsUIDs(t *testing.T) {
	importer := &fakeSafeSyncUserImporter{}

	reports, err := runSafeSyncImports(context.Background(), importer, []int64{9, 2}, false)
	require.NoError(t, err)
	require.Equal(t, []safeSyncImportCall{{uid: 2, dryRun: true}, {uid: 9, dryRun: true}}, importer.calls)
	require.Len(t, reports, 2)
	require.False(t, reports[0].Verified)
}

func TestRunSafeSyncImportsApplyAndStopOnFirstFailure(t *testing.T) {
	importer := &fakeSafeSyncUserImporter{failAt: 4}

	reports, err := runSafeSyncImports(context.Background(), importer, []int64{8, 4, 2}, true)
	require.ErrorContains(t, err, "uid 4")
	require.Nil(t, reports)
	require.Equal(t, []safeSyncImportCall{{uid: 2, dryRun: false}, {uid: 4, dryRun: false}}, importer.calls)
}

func TestListSafeSyncImportUIDsReadsOnlyActiveUsers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "main.sqlite3")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{UID: 9, Username: "active"}).Error)
	require.NoError(t, db.Create(&model.User{UID: 4, Username: "deleted", IsDeleted: 1}).Error)

	uids, err := listSafeSyncImportUIDs(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []int64{9}, uids)
}
