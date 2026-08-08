package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	TableNameSafeSyncMigrationState = "safe_sync_migration_state"
	TableNameVaultSyncState         = "vault_sync_state"
	TableNameSyncResourceMetadata   = "sync_resource_metadata"
	TableNameSyncPathTombstone      = "sync_path_tombstone"
	TableNameSyncEvent              = "sync_event"
	TableNameSyncOperation          = "sync_operation"
	TableNameDeviceSyncRole         = "device_sync_role"
	SafeSyncOperationRetention      = 30 * 24 * time.Hour
)

type SafeSyncMigrationState struct {
	ID           int64      `gorm:"column:id;primaryKey;autoIncrement:false"`
	Status       string     `gorm:"column:status;type:varchar(16);not null;index"`
	ReportDigest string     `gorm:"column:report_digest;type:varchar(64);not null;default:''"`
	VerifiedAt   *time.Time `gorm:"column:verified_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (*SafeSyncMigrationState) TableName() string { return TableNameSafeSyncMigrationState }

type VaultSyncState struct {
	ID                        int64      `gorm:"column:id;primaryKey;autoIncrement"`
	VaultID                   int64      `gorm:"column:vault_id;not null;uniqueIndex"`
	State                     string     `gorm:"column:state;type:varchar(32);not null;default:'OFF';index"`
	LatestVaultRevision       int64      `gorm:"column:latest_vault_revision;not null;default:0"`
	BootstrapSessionID        string     `gorm:"column:bootstrap_session_id;type:varchar(128);not null;default:''"`
	BootstrapDeviceID         string     `gorm:"column:bootstrap_device_id;type:varchar(128);not null;default:''"`
	BootstrapPreviousState    string     `gorm:"column:bootstrap_previous_state;type:varchar(32);not null;default:''"`
	BootstrapExpiresAt        *time.Time `gorm:"column:bootstrap_expires_at"`
	BootstrapManifestHash     string     `gorm:"column:bootstrap_manifest_hash;type:varchar(128);not null;default:''"`
	BootstrapSnapshotRevision int64      `gorm:"column:bootstrap_snapshot_revision;not null;default:0"`
	MigrationVerifiedAt       *time.Time `gorm:"column:migration_verified_at"`
	ActivatedAt               *time.Time `gorm:"column:activated_at"`
	CreatedAt                 time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt                 time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (*VaultSyncState) TableName() string { return TableNameVaultSyncState }

type SyncResourceMetadata struct {
	ResourceID       string    `gorm:"column:resource_id;type:varchar(64);primaryKey"`
	VaultID          int64     `gorm:"column:vault_id;not null;index:idx_sync_resource_vault_path,priority:1;index;uniqueIndex:uidx_sync_resource_live_path,priority:1,where:state = 'LIVE'"`
	ResourceType     string    `gorm:"column:resource_type;type:varchar(16);not null;uniqueIndex:idx_sync_resource_legacy,priority:1"`
	LegacyID         int64     `gorm:"column:legacy_id;not null;uniqueIndex:idx_sync_resource_legacy,priority:2"`
	ResourceRevision int64     `gorm:"column:resource_revision;not null;default:1"`
	CurrentPath      string    `gorm:"column:current_path;type:varchar(1024);not null;index:idx_sync_resource_vault_path,priority:2;uniqueIndex:uidx_sync_resource_live_path,priority:2,where:state = 'LIVE'"`
	CurrentPathHash  string    `gorm:"column:current_path_hash;type:varchar(1024);not null;default:''"`
	ContentHash      string    `gorm:"column:content_hash;type:varchar(255);not null;default:''"`
	State            string    `gorm:"column:state;type:varchar(16);not null;index;default:'LIVE'"`
	Size             int64     `gorm:"column:size;not null;default:0"`
	CreatedAt        time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt        time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (*SyncResourceMetadata) TableName() string { return TableNameSyncResourceMetadata }

type SyncPathTombstone struct {
	ID               int64     `gorm:"column:id;primaryKey;autoIncrement"`
	VaultID          int64     `gorm:"column:vault_id;not null;index:idx_sync_tombstone_vault_path,priority:1"`
	Path             string    `gorm:"column:path;type:varchar(1024);not null;index:idx_sync_tombstone_vault_path,priority:2"`
	PathHash         string    `gorm:"column:path_hash;type:varchar(1024);not null;default:''"`
	ResourceID       string    `gorm:"column:resource_id;type:varchar(64);not null;index"`
	ResourceRevision int64     `gorm:"column:resource_revision;not null"`
	VaultRevision    int64     `gorm:"column:vault_revision;not null"`
	TransactionID    string    `gorm:"column:transaction_id;type:varchar(64);not null;index"`
	CreatedAt        time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

func (*SyncPathTombstone) TableName() string { return TableNameSyncPathTombstone }

type SyncEvent struct {
	ID               int64     `gorm:"column:id;primaryKey;autoIncrement"`
	VaultID          int64     `gorm:"column:vault_id;not null;uniqueIndex:idx_sync_event_vault_revision,priority:1"`
	VaultRevision    int64     `gorm:"column:vault_revision;not null;uniqueIndex:idx_sync_event_vault_revision,priority:2"`
	ResourceID       string    `gorm:"column:resource_id;type:varchar(64);not null;index"`
	ResourceRevision int64     `gorm:"column:resource_revision;not null"`
	ResourceType     string    `gorm:"column:resource_type;type:varchar(16);not null"`
	Action           string    `gorm:"column:action;type:varchar(32);not null"`
	Path             string    `gorm:"column:path;type:varchar(1024);not null;default:''"`
	PreviousPath     string    `gorm:"column:previous_path;type:varchar(1024);not null;default:''"`
	ContentHash      string    `gorm:"column:content_hash;type:varchar(255);not null;default:''"`
	State            string    `gorm:"column:state;type:varchar(16);not null;default:'LIVE'"`
	TransactionID    string    `gorm:"column:transaction_id;type:varchar(64);not null;index"`
	OperationID      string    `gorm:"column:operation_id;type:varchar(128);not null;index"`
	CreatedAt        time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

func (*SyncEvent) TableName() string { return TableNameSyncEvent }

type SyncOperation struct {
	ID                 int64     `gorm:"column:id;primaryKey;autoIncrement"`
	VaultID            int64     `gorm:"column:vault_id;not null;uniqueIndex:idx_sync_operation_identity,priority:1"`
	DeviceID           string    `gorm:"column:device_id;type:varchar(128);not null;uniqueIndex:idx_sync_operation_identity,priority:2"`
	OperationID        string    `gorm:"column:operation_id;type:varchar(128);not null;uniqueIndex:idx_sync_operation_identity,priority:3"`
	Action             string    `gorm:"column:action;type:varchar(32);not null;default:''"`
	RequestFingerprint string    `gorm:"column:request_fingerprint;type:varchar(128);not null"`
	State              string    `gorm:"column:state;type:varchar(16);not null;index"`
	ResourceID         string    `gorm:"column:resource_id;type:varchar(64);not null;default:''"`
	LegacyID           int64     `gorm:"column:legacy_id;not null;default:0"`
	ResourceRevision   int64     `gorm:"column:resource_revision;not null;default:0"`
	VaultRevision      int64     `gorm:"column:vault_revision;not null;default:0"`
	ContentHash        string    `gorm:"column:content_hash;type:varchar(255);not null;default:''"`
	Outcome            string    `gorm:"column:outcome;type:text;not null;default:''"`
	ErrorCode          string    `gorm:"column:error_code;type:varchar(64);not null;default:''"`
	RequestPayload     string    `gorm:"column:request_payload;type:text;not null;default:''"`
	StagedPath         string    `gorm:"column:staged_path;type:text;not null;default:''"`
	OldImagePath       string    `gorm:"column:old_image_path;type:text;not null;default:''"`
	TargetPath         string    `gorm:"column:target_path;type:text;not null;default:''"`
	ExpectedHash       string    `gorm:"column:expected_hash;type:varchar(255);not null;default:''"`
	TargetExisted      bool      `gorm:"column:target_existed;not null;default:false"`
	ExpiresAt          time.Time `gorm:"column:expires_at;not null;index"`
	CreatedAt          time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

type DeviceSyncRole struct {
	ID             int64      `gorm:"column:id;primaryKey;autoIncrement"`
	VaultID        int64      `gorm:"column:vault_id;not null;uniqueIndex:idx_device_sync_role_identity,priority:1;index"`
	DeviceID       string     `gorm:"column:device_id;type:varchar(128);not null;uniqueIndex:idx_device_sync_role_identity,priority:2"`
	Role           string     `gorm:"column:role;type:varchar(32);not null;default:'BIDIRECTIONAL';index"`
	LeaseExpiresAt *time.Time `gorm:"column:lease_expires_at;index"`
	LastSeenAt     time.Time  `gorm:"column:last_seen_at;not null"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (*DeviceSyncRole) TableName() string { return TableNameDeviceSyncRole }

func (*SyncOperation) TableName() string { return TableNameSyncOperation }

func (o *SyncOperation) BeforeCreate(*gorm.DB) error {
	if o.ExpiresAt.IsZero() {
		o.ExpiresAt = time.Now().UTC().Add(SafeSyncOperationRetention)
	}
	return nil
}

func AutoMigrateSafeSync(db *gorm.DB) error {
	return db.AutoMigrate(
		&SafeSyncMigrationState{},
		&VaultSyncState{},
		&SyncResourceMetadata{},
		&SyncPathTombstone{},
		&SyncEvent{},
		&SyncOperation{},
		&DeviceSyncRole{},
	)
}
