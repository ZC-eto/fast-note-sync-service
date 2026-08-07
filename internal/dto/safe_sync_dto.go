package dto

type SafeSyncStatusRequest struct {
	Vault string `json:"vault" binding:"required"`
}

type SafeSyncStatusResponse struct {
	Capability          bool   `json:"capability"`
	State               string `json:"state"`
	LatestVaultRevision int64  `json:"latestVaultRevision"`
	MigrationVerified   bool   `json:"migrationVerified"`
	BootstrapSessionID  string `json:"bootstrapSessionId,omitempty"`
	BootstrapExpiresAt  int64  `json:"bootstrapExpiresAt,omitempty"`
	UID                 int64  `json:"uid"`
	VaultID             int64  `json:"vaultId"`
}

type SafeSyncBootstrapStartRequest struct {
	Vault    string `json:"vault" binding:"required"`
	DeviceID string `json:"deviceId" binding:"required"`
	Context  string `json:"context"`
}

type SafeSyncBootstrapStartResponse struct {
	State                 string `json:"state"`
	SessionID             string `json:"sessionId"`
	ExpiresAt             int64  `json:"expiresAt"`
	SnapshotVaultRevision int64  `json:"snapshotVaultRevision"`
	ManifestHash          string `json:"manifestHash"`
	ResourceCount         int64  `json:"resourceCount"`
	Cursor                string `json:"cursor,omitempty"`
}

type SafeSyncBootstrapPageRequest struct {
	Vault     string `json:"vault" binding:"required"`
	SessionID string `json:"sessionId" binding:"required"`
	Cursor    string `json:"cursor" binding:"required"`
	PageSize  int32  `json:"pageSize"`
	Context   string `json:"context"`
}

type SafeSyncManifestItem struct {
	ResourceID       string `json:"resourceId"`
	ResourceType     string `json:"resourceType"`
	Path             string `json:"path"`
	PathHash         string `json:"pathHash"`
	State            string `json:"state"`
	ResourceRevision int64  `json:"resourceRevision"`
	ContentHash      string `json:"contentHash"`
	Size             int64  `json:"size"`
}

type SafeSyncBootstrapPageResponse struct {
	SessionID             string                 `json:"sessionId"`
	SnapshotVaultRevision int64                  `json:"snapshotVaultRevision"`
	ManifestHash          string                 `json:"manifestHash"`
	Items                 []SafeSyncManifestItem `json:"items"`
	NextCursor            string                 `json:"nextCursor,omitempty"`
}

type SafeSyncBootstrapCommitRequest struct {
	Vault                 string `json:"vault" binding:"required"`
	SessionID             string `json:"sessionId" binding:"required"`
	ManifestHash          string `json:"manifestHash" binding:"required"`
	SnapshotVaultRevision int64  `json:"snapshotVaultRevision"`
	Context               string `json:"context"`
}

type SafeSyncBootstrapCancelRequest struct {
	Vault     string `json:"vault" binding:"required"`
	SessionID string `json:"sessionId" binding:"required"`
	Context   string `json:"context"`
}

type SafeSyncEventsRequest struct {
	Vault         string `json:"vault" binding:"required"`
	AfterRevision int64  `json:"afterRevision"`
	PageSize      int32  `json:"pageSize"`
	Context       string `json:"context"`
}

type SafeSyncEvent struct {
	VaultRevision    int64  `json:"vaultRevision"`
	ResourceID       string `json:"resourceId"`
	ResourceRevision int64  `json:"resourceRevision"`
	ResourceType     string `json:"resourceType"`
	Action           string `json:"action"`
	Path             string `json:"path"`
	PreviousPath     string `json:"previousPath"`
	ContentHash      string `json:"contentHash"`
	State            string `json:"state"`
	TransactionID    string `json:"transactionId"`
	OperationID      string `json:"operationId"`
}

type SafeSyncEventsResponse struct {
	Events              []SafeSyncEvent `json:"events"`
	LatestVaultRevision int64           `json:"latestVaultRevision"`
	NextRevision        int64           `json:"nextRevision"`
	HasMore             bool            `json:"hasMore"`
}

type SafeMutationRequest struct {
	Vault             string `json:"vault" binding:"required"`
	Context           string `json:"context"`
	DeviceID          string `json:"deviceId" binding:"required"`
	OperationID       string `json:"operationId" binding:"required"`
	ResourceID        string `json:"resourceId"`
	BaseRevision      int64  `json:"baseRevision"`
	BaseHash          string `json:"baseHash"`
	ExpectedPathState string `json:"expectedPathState" binding:"required"`
	Action            string `json:"action" binding:"required"`
	Path              string `json:"path" binding:"required"`
	PathHash          string `json:"pathHash" binding:"required"`
	PreviousPath      string `json:"previousPath"`
	PreviousPathHash  string `json:"previousPathHash"`
	Content           string `json:"content"`
	ContentHash       string `json:"contentHash"`
	Size              int64  `json:"size"`
	Ctime             int64  `json:"ctime"`
	Mtime             int64  `json:"mtime"`
}

type SafeMutationResponse struct {
	ResourceID       string `json:"resourceId"`
	ResourceRevision int64  `json:"resourceRevision"`
	VaultRevision    int64  `json:"vaultRevision"`
	ContentHash      string `json:"contentHash"`
	Outcome          string `json:"outcome"`
	Replayed         bool   `json:"-"`
}

type SafeSyncErrorData struct {
	ErrorCode        string `json:"errorCode"`
	ResourceID       string `json:"resourceId,omitempty"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
	ActualRevision   int64  `json:"actualRevision,omitempty"`
	CurrentHash      string `json:"currentHash,omitempty"`
	CurrentPath      string `json:"currentPath,omitempty"`
	CurrentPathState string `json:"currentPathState,omitempty"`
	OperationID      string `json:"operationId,omitempty"`
	Retryable        bool   `json:"retryable,omitempty"`
}

type SafeFileUploadStartRequest struct {
	SafeMutationRequest
	ChunkSize int64 `json:"chunkSize"`
}

type SafeFileUploadStartResponse struct {
	SessionID      string `json:"sessionId"`
	NextChunkIndex int64  `json:"nextChunkIndex"`
	OperationID    string `json:"operationId"`
	ExpiresAt      int64  `json:"expiresAt"`
}

type SafeFileUploadCommitRequest struct {
	Vault       string `json:"vault" binding:"required"`
	Context     string `json:"context"`
	DeviceID    string `json:"deviceId" binding:"required"`
	OperationID string `json:"operationId" binding:"required"`
	SessionID   string `json:"sessionId" binding:"required"`
	ContentHash string `json:"contentHash" binding:"required"`
	Size        int64  `json:"size"`
}
