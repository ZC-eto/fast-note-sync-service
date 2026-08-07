package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigPreservesExplicitFalseAndEmptyDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
app:
  ws-parallel-enabled: false
  ws-check-utf8-enabled: false
  ws-compression-enabled: false
security:
  webgui-login-token-expiry:
  webgui-login-token-bind-ip: false
tracer:
  enabled: false
storage:
  local-fs:
    httpfs-is-enable: false
  aliyun-oss:
    is-enable: false
  aws-s3:
    is-enable:
database:
  auto-migrate: false
user-database:
  auto-migrate: false
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)

	require.False(t, *cfg.App.WebSocketParallelEnabled)
	require.False(t, *cfg.App.WebSocketCheckUtf8Enabled)
	require.False(t, *cfg.App.WebSocketCompressionEnabled)
	require.False(t, *cfg.Security.WebGUILoginTokenBindIP)
	require.False(t, *cfg.Tracer.Enabled)
	require.False(t, *cfg.Storage.LocalFS.HttpfsIsEnable)
	require.False(t, *cfg.Storage.AliyunOSS.IsEnabled)
	require.False(t, *cfg.Database.AutoMigrate)
	require.False(t, *cfg.UserDatabase.AutoMigrate)

	require.Equal(t, "7d", cfg.Security.WebGUILoginTokenExpiry)
	require.True(t, *cfg.Storage.AwsS3.IsEnabled)
	require.True(t, *cfg.Storage.MinIO.IsEnabled)
}

func TestLoadConfigMCPDisableLocalhostProtection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
server:
  mcp-disable-localhost-protection: true
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.True(t, cfg.Server.MCPDisableLocalhostProtection)
}

func TestLoadConfigAppliesUserDatabaseEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
database:
  type: sqlite
  path: storage/database/db.sqlite3
user-database:
  type: sqlite
  host: old-host
  port: 1111
  username: old-user
  password: old-password
  name: old-database
  ssl-mode: require
`), 0644)
	require.NoError(t, err)

	t.Setenv("FNS_USER_DATABASE_TYPE", "postgres")
	t.Setenv("FNS_USER_DATABASE_HOST", "postgres.internal")
	t.Setenv("FNS_USER_DATABASE_PORT", "5432")
	t.Setenv("FNS_USER_DATABASE_USERNAME", "fast_note_sync")
	t.Setenv("FNS_USER_DATABASE_PASSWORD", "secret")
	t.Setenv("FNS_USER_DATABASE_NAME", "fast_note_sync")
	t.Setenv("FNS_USER_DATABASE_SSL_MODE", "disable")
	t.Setenv("FNS_USER_DATABASE_SCHEMA", "public")

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.Equal(t, "sqlite", cfg.Database.Type)
	require.Equal(t, "storage/database/db.sqlite3", cfg.Database.Path)
	require.Equal(t, "postgres", cfg.UserDatabase.Type)
	require.Equal(t, "postgres.internal", cfg.UserDatabase.Host)
	require.Equal(t, 5432, cfg.UserDatabase.Port)
	require.Equal(t, "fast_note_sync", cfg.UserDatabase.UserName)
	require.Equal(t, "secret", cfg.UserDatabase.Password)
	require.Equal(t, "fast_note_sync", cfg.UserDatabase.Name)
	require.Equal(t, "disable", cfg.UserDatabase.SSLMode)
	require.Equal(t, "public", cfg.UserDatabase.Schema)
}

func TestLoadConfigRejectsInvalidUserDatabaseEnvironmentPort(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("user-database: {}\n"), 0644))
	t.Setenv("FNS_USER_DATABASE_PORT", "not-a-port")

	_, _, err := LoadConfig(configPath)
	require.ErrorContains(t, err, "FNS_USER_DATABASE_PORT")
}
