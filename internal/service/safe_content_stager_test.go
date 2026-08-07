package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestSafeContentStager_RecoversAppliedContentAfterCrash(t *testing.T) {
	volumeRoot := t.TempDir()
	targetPath := filepath.Join(volumeRoot, "notes", "a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, []byte("old content"), 0o600))

	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	prepared, err := stager.Stage(context.Background(), "operation-1", targetPath, []byte("new content"), util.EncodeHash32("new content"))
	require.NoError(t, err)
	require.NoError(t, stager.Apply(context.Background(), prepared))

	recovery, err := stager.Recover(context.Background(), prepared)
	require.NoError(t, err)
	require.Equal(t, SafeContentRecoveryApplied, recovery)
	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, "new content", string(content))
}

func TestSafeContentStager_RestoresOldImageWhenReplacementIsIncomplete(t *testing.T) {
	volumeRoot := t.TempDir()
	targetPath := filepath.Join(volumeRoot, "files", "a.bin")
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, []byte("old"), 0o600))

	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	prepared, err := stager.Stage(context.Background(), "operation-2", targetPath, []byte("new"), util.EncodeHash32Bytes([]byte("new")))
	require.NoError(t, err)

	// Simulate a crash after the old image was moved aside but before staged content reached target.
	require.NoError(t, os.Rename(targetPath, prepared.OldImagePath))
	recovery, err := stager.Recover(context.Background(), prepared)
	require.NoError(t, err)
	require.Equal(t, SafeContentRecoveryRestored, recovery)
	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, "old", string(content))
}

func TestSafeContentStager_RejectsHashMismatchWithoutChangingTarget(t *testing.T) {
	volumeRoot := t.TempDir()
	targetPath := filepath.Join(volumeRoot, "a.md")
	require.NoError(t, os.WriteFile(targetPath, []byte("old"), 0o600))

	stager, err := NewSafeContentStager(filepath.Join(volumeRoot, ".safe-sync-staging"), volumeRoot)
	require.NoError(t, err)
	_, err = stager.Stage(context.Background(), "operation-3", targetPath, []byte("new"), "wrong-hash")
	require.Error(t, err)
	content, readErr := os.ReadFile(targetPath)
	require.NoError(t, readErr)
	require.Equal(t, "old", string(content))
}
