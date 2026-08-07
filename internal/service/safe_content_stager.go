package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

type SafeContentRecovery string

const (
	SafeContentRecoveryApplied  SafeContentRecovery = "APPLIED"
	SafeContentRecoveryRestored SafeContentRecovery = "RESTORED"
)

type StagedContent struct {
	OperationID   string
	TargetPath    string
	StagedPath    string
	OldImagePath  string
	ExpectedHash  string
	TargetExisted bool
}

type SafeContentStager struct {
	stagingRoot string
	volumeRoot  string
}

func NewSafeContentStager(stagingRoot, volumeRoot string) (*SafeContentStager, error) {
	if stagingRoot == "" || volumeRoot == "" {
		return nil, errors.New("staging root and volume root are required")
	}
	absStaging, err := filepath.Abs(stagingRoot)
	if err != nil {
		return nil, err
	}
	absVolume, err := filepath.Abs(volumeRoot)
	if err != nil {
		return nil, err
	}
	if !pathWithinRoot(absStaging, absVolume) {
		return nil, errors.New("safe sync staging must be on the target volume")
	}
	return &SafeContentStager{stagingRoot: absStaging, volumeRoot: absVolume}, nil
}

func (s *SafeContentStager) Stage(ctx context.Context, operationID, targetPath string, content []byte, expectedHash string) (*StagedContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operationID == "" || expectedHash == "" {
		return nil, errors.New("operation id and expected hash are required")
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, err
	}
	if !pathWithinRoot(absTarget, s.volumeRoot) {
		return nil, errors.New("safe sync target is outside the persistent volume")
	}
	if !matchesSafeContentHash(content, expectedHash) {
		return nil, errors.New("staged content hash mismatch")
	}

	operationDir := filepath.Join(s.stagingRoot, util.EncodeMD5(operationID))
	if err := os.MkdirAll(operationDir, 0o700); err != nil {
		return nil, err
	}
	stagedPath := filepath.Join(operationDir, "content.new")
	oldImagePath := filepath.Join(operationDir, "content.old")
	if err := writeDurableFile(stagedPath, content); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(absTarget)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	return &StagedContent{
		OperationID:   operationID,
		TargetPath:    absTarget,
		StagedPath:    stagedPath,
		OldImagePath:  oldImagePath,
		ExpectedHash:  expectedHash,
		TargetExisted: statErr == nil,
	}, nil
}

func (s *SafeContentStager) Apply(ctx context.Context, prepared *StagedContent) error {
	if err := s.validatePrepared(ctx, prepared); err != nil {
		return err
	}
	if matchesFileHash(prepared.TargetPath, prepared.ExpectedHash) {
		return nil
	}
	if !matchesFileHash(prepared.StagedPath, prepared.ExpectedHash) {
		return errors.New("staged content is missing or corrupt")
	}
	if err := os.MkdirAll(filepath.Dir(prepared.TargetPath), 0o755); err != nil {
		return err
	}

	if prepared.TargetExisted {
		if err := os.Rename(prepared.TargetPath, prepared.OldImagePath); err != nil {
			return fmt.Errorf("move old image: %w", err)
		}
	}
	if err := os.Rename(prepared.StagedPath, prepared.TargetPath); err != nil {
		if prepared.TargetExisted {
			_ = os.Rename(prepared.OldImagePath, prepared.TargetPath)
		}
		return fmt.Errorf("apply staged content: %w", err)
	}
	return syncDirectory(filepath.Dir(prepared.TargetPath))
}

func (s *SafeContentStager) Recover(ctx context.Context, prepared *StagedContent) (SafeContentRecovery, error) {
	if err := s.validatePrepared(ctx, prepared); err != nil {
		return "", err
	}
	if matchesFileHash(prepared.TargetPath, prepared.ExpectedHash) {
		return SafeContentRecoveryApplied, nil
	}

	if _, err := os.Stat(prepared.OldImagePath); err == nil {
		if removeErr := os.Remove(prepared.TargetPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return "", removeErr
		}
		if err := os.Rename(prepared.OldImagePath, prepared.TargetPath); err != nil {
			return "", fmt.Errorf("restore old image: %w", err)
		}
		return SafeContentRecoveryRestored, syncDirectory(filepath.Dir(prepared.TargetPath))
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if prepared.TargetExisted {
		if _, err := os.Stat(prepared.TargetPath); err == nil {
			return SafeContentRecoveryRestored, nil
		}
		return "", errors.New("old image is unavailable for recovery")
	}
	if err := os.Remove(prepared.TargetPath); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return SafeContentRecoveryRestored, nil
}

func (s *SafeContentStager) Finalize(prepared *StagedContent) error {
	if prepared == nil || prepared.StagedPath == "" {
		return errors.New("prepared content is required")
	}
	return os.RemoveAll(filepath.Dir(prepared.StagedPath))
}

func (s *SafeContentStager) validatePrepared(ctx context.Context, prepared *StagedContent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if prepared == nil {
		return errors.New("prepared content is required")
	}
	if !pathWithinRoot(prepared.TargetPath, s.volumeRoot) || !pathWithinRoot(prepared.StagedPath, s.stagingRoot) || !pathWithinRoot(prepared.OldImagePath, s.stagingRoot) {
		return errors.New("prepared content paths are outside safe sync roots")
	}
	return nil
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func matchesSafeContentHash(content []byte, expected string) bool {
	return util.EncodeHash32Bytes(content) == expected || util.EncodeHash32(string(content)) == expected
}

func matchesFileHash(path, expected string) bool {
	content, err := os.ReadFile(path)
	return err == nil && matchesSafeContentHash(content, expected)
}

func writeDurableFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Sync()
	if err != nil && runtime.GOOS == "windows" {
		return nil
	}
	return err
}
