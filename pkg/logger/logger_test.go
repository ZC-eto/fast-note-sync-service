package logger

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
)

func TestLoggerInitialization(t *testing.T) {
	// Nop logger
	assert.NotNil(t, Nop())

	// Global Logger
	assert.NotNil(t, L())
	assert.NotNil(t, S())

	// SetLevel
	SetLevel(zapcore.DebugLevel)
}

func TestNewLogger(t *testing.T) {
	logFile := os.DevNull
	if runtime.GOOS != "windows" {
		logFile = filepath.Join(t.TempDir(), "test.log")
	}

	cfg := Config{
		Level:      "info",
		File:       logFile,
		Production: false,
	}

	log, err := NewLogger(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, log)

	log.Info("test info")
	log.Sync()

	if logFile != os.DevNull {
		stat, err := os.Stat(logFile)
		assert.NoError(t, err)
		assert.True(t, stat.Size() > 0)
	}
}
