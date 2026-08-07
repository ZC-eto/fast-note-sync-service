package domain

import "time"

type SafeSyncErrorCode string

const (
	SafeSyncErrorUnsupported            SafeSyncErrorCode = "SAFE_SYNC_UNSUPPORTED"
	SafeSyncErrorBootstrapInProgress    SafeSyncErrorCode = "SAFE_SYNC_BOOTSTRAP_IN_PROGRESS"
	SafeSyncErrorStrictRequired         SafeSyncErrorCode = "SAFE_SYNC_STRICT_REQUIRED"
	SafeSyncErrorRevisionConflict       SafeSyncErrorCode = "REVISION_CONFLICT"
	SafeSyncErrorPathStateConflict      SafeSyncErrorCode = "PATH_STATE_CONFLICT"
	SafeSyncErrorOperationIDReused      SafeSyncErrorCode = "OPERATION_ID_REUSED"
	SafeSyncErrorOperationExpired       SafeSyncErrorCode = "OPERATION_EXPIRED"
	SafeSyncErrorRebootstrapRequired    SafeSyncErrorCode = "REBOOTSTRAP_REQUIRED"
	SafeSyncErrorBootstrapStateConflict SafeSyncErrorCode = "BOOTSTRAP_STATE_CONFLICT"
)

type SafeSyncError struct {
	Code    SafeSyncErrorCode
	Message string
}

func (e *SafeSyncError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

type VaultSyncState string

const (
	VaultSyncStateOff           VaultSyncState = "OFF"
	VaultSyncStateBootstrapping VaultSyncState = "BOOTSTRAPPING"
	VaultSyncStateStrict        VaultSyncState = "STRICT"
)

type SyncResourceType string

const (
	SyncResourceTypeNote   SyncResourceType = "NOTE"
	SyncResourceTypeFile   SyncResourceType = "FILE"
	SyncResourceTypeFolder SyncResourceType = "FOLDER"
)

type SyncResourceState string

const (
	SyncResourceStateLive    SyncResourceState = "LIVE"
	SyncResourceStateDeleted SyncResourceState = "DELETED"
)

type ExpectedPathState string

const (
	ExpectedPathStateAbsent  ExpectedPathState = "ABSENT"
	ExpectedPathStatePresent ExpectedPathState = "PRESENT"
)

type SyncOperationState string

const (
	SyncOperationStatePrepared  SyncOperationState = "PREPARED"
	SyncOperationStateCommitted SyncOperationState = "COMMITTED"
	SyncOperationStateRejected  SyncOperationState = "REJECTED"
)

type MutationPrecondition struct {
	ResourceID        string
	BaseRevision      int64
	BaseHash          string
	ExpectedPathState ExpectedPathState
}

type MutationIdentity struct {
	DeviceID    string
	OperationID string
}

type MutationResult struct {
	ResourceID       string
	ResourceRevision int64
	VaultRevision    int64
	ContentHash      string
	Outcome          string
}

type SyncResourceMetadata struct {
	ResourceID       string
	VaultID          int64
	ResourceType     SyncResourceType
	LegacyID         int64
	ResourceRevision int64
	CurrentPath      string
	CurrentPathHash  string
	ContentHash      string
	State            SyncResourceState
	Size             int64
}

type SyncPathTombstone struct {
	VaultID          int64
	Path             string
	PathHash         string
	ResourceID       string
	ResourceRevision int64
	VaultRevision    int64
	TransactionID    string
}

type SyncEvent struct {
	VaultID          int64
	VaultRevision    int64
	ResourceID       string
	ResourceRevision int64
	ResourceType     SyncResourceType
	Action           string
	Path             string
	PreviousPath     string
	ContentHash      string
	State            SyncResourceState
	TransactionID    string
	OperationID      string
}

type SyncOperation struct {
	VaultID            int64
	Identity           MutationIdentity
	Action             string
	RequestFingerprint string
	State              SyncOperationState
	Result             MutationResult
	ErrorCode          string
	LegacyID           int64
	RequestPayload     string
	StagedPath         string
	OldImagePath       string
	TargetPath         string
	ExpectedHash       string
	TargetExisted      bool
	ExpiresAt          time.Time
}
