package websocket_router

import (
	"errors"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSafeSyncProtobufRequests(t *testing.T) {
	tests := []struct {
		name   string
		action WebSocketReceiveAction
		wire   proto.Message
		dest   any
		assert func(*testing.T, any)
	}{
		{
			name: "status", action: SafeSyncReceiveStatus,
			wire: &v1.SafeSyncStatusRequest{Vault: "vault-a"}, dest: &dto.SafeSyncStatusRequest{},
			assert: func(t *testing.T, dest any) {
				require.Equal(t, "vault-a", dest.(*dto.SafeSyncStatusRequest).Vault)
			},
		},
		{
			name: "device role", action: SafeSyncReceiveDeviceRoleRegister,
			wire: &v1.DeviceRoleRegisterRequest{Vault: "vault-a", DeviceId: "device-a", Role: "LOCAL_PUBLISHER", Context: "ctx-a"}, dest: &dto.DeviceRoleRegisterRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.DeviceRoleRegisterRequest)
				require.Equal(t, "device-a", got.DeviceID)
				require.Equal(t, "LOCAL_PUBLISHER", got.Role)
				require.Equal(t, "ctx-a", got.Context)
			},
		},
		{
			name: "bootstrap start", action: SafeSyncReceiveBootstrapStart,
			wire: &v1.SafeSyncBootstrapStartRequest{Vault: "vault-a", DeviceId: "device-a", Context: "ctx-a"}, dest: &dto.SafeSyncBootstrapStartRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeSyncBootstrapStartRequest)
				require.Equal(t, "device-a", got.DeviceID)
				require.Equal(t, "ctx-a", got.Context)
			},
		},
		{
			name: "bootstrap page", action: SafeSyncReceiveBootstrapPage,
			wire: &v1.SafeSyncBootstrapPageRequest{Vault: "vault-a", SessionId: "session-a", Cursor: "cursor-a", PageSize: 250}, dest: &dto.SafeSyncBootstrapPageRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeSyncBootstrapPageRequest)
				require.Equal(t, "session-a", got.SessionID)
				require.Equal(t, int32(250), got.PageSize)
			},
		},
		{
			name: "bootstrap commit", action: SafeSyncReceiveBootstrapCommit,
			wire: &v1.SafeSyncBootstrapCommitRequest{Vault: "vault-a", SessionId: "session-a", ManifestHash: "manifest-a", SnapshotVaultRevision: 17}, dest: &dto.SafeSyncBootstrapCommitRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeSyncBootstrapCommitRequest)
				require.Equal(t, "manifest-a", got.ManifestHash)
				require.Equal(t, int64(17), got.SnapshotVaultRevision)
			},
		},
		{
			name: "bootstrap cancel", action: SafeSyncReceiveBootstrapCancel,
			wire: &v1.SafeSyncBootstrapCancelRequest{Vault: "vault-a", SessionId: "session-a"}, dest: &dto.SafeSyncBootstrapCancelRequest{},
			assert: func(t *testing.T, dest any) {
				require.Equal(t, "session-a", dest.(*dto.SafeSyncBootstrapCancelRequest).SessionID)
			},
		},
		{
			name: "events", action: SafeSyncReceiveEvents,
			wire: &v1.SafeSyncEventsRequest{Vault: "vault-a", AfterRevision: 19, PageSize: 100}, dest: &dto.SafeSyncEventsRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeSyncEventsRequest)
				require.Equal(t, int64(19), got.AfterRevision)
				require.Equal(t, int32(100), got.PageSize)
			},
		},
		{
			name: "note mutation", action: SafeSyncReceiveNoteMutation,
			wire: &v1.SafeMutationRequest{Vault: "vault-a", DeviceId: "device-a", OperationId: "op-a", ResourceId: "resource-a", BaseRevision: 7, ExpectedPathState: "PRESENT", Action: "MODIFY", Path: "a.md", PathHash: "path-a", Content: "body", ContentHash: "hash-a"}, dest: &dto.SafeMutationRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeMutationRequest)
				require.Equal(t, "op-a", got.OperationID)
				require.Equal(t, int64(7), got.BaseRevision)
				require.Equal(t, "body", got.Content)
			},
		},
		{
			name: "folder mutation", action: SafeSyncReceiveFolderMutation,
			wire: &v1.SafeMutationRequest{Vault: "vault-a", DeviceId: "device-a", OperationId: "op-folder", ExpectedPathState: "ABSENT", Action: "CREATE", Path: "folder", PathHash: "path-folder"}, dest: &dto.SafeMutationRequest{},
			assert: func(t *testing.T, dest any) {
				require.Equal(t, "op-folder", dest.(*dto.SafeMutationRequest).OperationID)
			},
		},
		{
			name: "file mutation", action: SafeSyncReceiveFileMutation,
			wire: &v1.SafeMutationRequest{Vault: "vault-a", DeviceId: "device-a", OperationId: "op-file-delete", ResourceId: "resource-file", BaseRevision: 3, ExpectedPathState: "LIVE", Action: "DELETE", Path: "asset.bin", PathHash: "path-file"}, dest: &dto.SafeMutationRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeMutationRequest)
				require.Equal(t, "op-file-delete", got.OperationID)
				require.Equal(t, "DELETE", got.Action)
			},
		},
		{
			name: "file upload start", action: SafeSyncReceiveFileUploadStart,
			wire: &v1.SafeFileUploadStartRequest{Mutation: &v1.SafeMutationRequest{Vault: "vault-a", DeviceId: "device-a", OperationId: "op-file", ExpectedPathState: "ABSENT", Action: "CREATE", Path: "asset.bin", PathHash: "path-file", ContentHash: "hash-file", Size: 1024}, ChunkSize: 256}, dest: &dto.SafeFileUploadStartRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeFileUploadStartRequest)
				require.Equal(t, "op-file", got.OperationID)
				require.Equal(t, int64(256), got.ChunkSize)
			},
		},
		{
			name: "file upload commit", action: SafeSyncReceiveFileUploadCommit,
			wire: &v1.SafeFileUploadCommitRequest{Vault: "vault-a", DeviceId: "device-a", OperationId: "op-file", SessionId: "upload-a", ContentHash: "hash-file", Size: 1024}, dest: &dto.SafeFileUploadCommitRequest{},
			assert: func(t *testing.T, dest any) {
				got := dest.(*dto.SafeFileUploadCommitRequest)
				require.Equal(t, "upload-a", got.SessionID)
				require.Equal(t, int64(1024), got.Size)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := proto.Marshal(test.wire)
			require.NoError(t, err)
			decoded, err := DeReceiveProtobufToDTO(test.action, wire, test.dest)
			require.NoError(t, err)
			require.True(t, decoded)
			test.assert(t, test.dest)
		})
	}
}

func TestSafeSyncProtobufResponsesAndStructuredError(t *testing.T) {
	statusWire := decodeSafeSyncResponseData(t, SafeSyncStatusAck, dto.SafeSyncStatusResponse{
		Capability: true, State: "STRICT", LatestVaultRevision: 21, MigrationVerified: true, UID: 3, VaultID: 9,
	})
	var status v1.SafeSyncStatusResponse
	require.NoError(t, proto.Unmarshal(statusWire, &status))
	require.True(t, status.Capability)
	require.Equal(t, "STRICT", status.State)
	require.Equal(t, int64(21), status.LatestVaultRevision)
	require.Equal(t, int64(3), status.Uid)
	require.Equal(t, int64(9), status.VaultId)

	roleWire := decodeSafeSyncResponseData(t, SafeSyncDeviceRoleStatusAck, dto.DeviceRoleStatusResponse{
		DeviceID: "device-a", Role: "LOCAL_PUBLISHER", PublisherDeviceID: "device-a",
		PublisherLeaseExpiresAt: 1234, Writable: true,
	})
	var role v1.DeviceRoleStatusResponse
	require.NoError(t, proto.Unmarshal(roleWire, &role))
	require.Equal(t, "LOCAL_PUBLISHER", role.Role)
	require.Equal(t, "device-a", role.PublisherDeviceId)
	require.True(t, role.Writable)

	startWire := decodeSafeSyncResponseData(t, SafeSyncBootstrapStartAck, dto.SafeSyncBootstrapStartResponse{
		State: "BOOTSTRAPPING", SessionID: "session-a", ExpiresAt: 1000,
		SnapshotVaultRevision: 21, ManifestHash: "manifest-a", ResourceCount: 1, Cursor: "cursor-a",
	})
	var start v1.SafeSyncBootstrapStartResponse
	require.NoError(t, proto.Unmarshal(startWire, &start))
	require.Equal(t, "session-a", start.SessionId)
	require.Equal(t, "cursor-a", start.Cursor)

	pageWire := decodeSafeSyncResponseData(t, SafeSyncBootstrapPageAck, dto.SafeSyncBootstrapPageResponse{
		SessionID: "session-a", SnapshotVaultRevision: 21, ManifestHash: "manifest-a",
		Items: []dto.SafeSyncManifestItem{{ResourceID: "resource-a", ResourceType: "NOTE", Path: "a.md", State: "LIVE", ResourceRevision: 7, ContentHash: "hash-a", Size: 12}},
	})
	var page v1.SafeSyncBootstrapPageResponse
	require.NoError(t, proto.Unmarshal(pageWire, &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, int64(7), page.Items[0].ResourceRevision)

	eventsWire := decodeSafeSyncResponseData(t, SafeSyncEventsAck, dto.SafeSyncEventsResponse{
		Events:              []dto.SafeSyncEvent{{VaultRevision: 22, ResourceID: "resource-a", ResourceRevision: 8, Action: "MODIFY"}},
		LatestVaultRevision: 22, NextRevision: 22,
	})
	var events v1.SafeSyncEventsResponse
	require.NoError(t, proto.Unmarshal(eventsWire, &events))
	require.Len(t, events.Events, 1)
	require.Equal(t, int64(22), events.Events[0].VaultRevision)

	mutationWire := decodeSafeSyncResponseData(t, SafeSyncNoteMutationAck, dto.SafeMutationResponse{
		ResourceID: "resource-a", ResourceRevision: 8, VaultRevision: 22, ContentHash: "hash-b", Outcome: "COMMITTED",
	})
	var mutation v1.SafeMutationResponse
	require.NoError(t, proto.Unmarshal(mutationWire, &mutation))
	require.Equal(t, "COMMITTED", mutation.Outcome)
	fileMutationWire := decodeSafeSyncResponseData(t, SafeSyncFileMutationAck, dto.SafeMutationResponse{
		ResourceID: "resource-file", ResourceRevision: 4, VaultRevision: 23, Outcome: "COMMITTED",
	})
	var fileMutation v1.SafeMutationResponse
	require.NoError(t, proto.Unmarshal(fileMutationWire, &fileMutation))
	require.Equal(t, "resource-file", fileMutation.ResourceId)

	uploadWire := decodeSafeSyncResponseData(t, SafeSyncFileUploadStartAck, dto.SafeFileUploadStartResponse{
		SessionID: "upload-a", NextChunkIndex: 3, OperationID: "op-file", ExpiresAt: 2000,
	})
	var upload v1.SafeFileUploadStartResponse
	require.NoError(t, proto.Unmarshal(uploadWire, &upload))
	require.Equal(t, int64(3), upload.NextChunkIndex)

	errorWire := decodeSafeSyncResponseData(t, SafeSyncNoteMutationAck, dto.SafeSyncErrorData{
		ErrorCode: string(domain.SafeSyncErrorRevisionConflict), ResourceID: "resource-a", ExpectedRevision: 7, ActualRevision: 8,
	})
	var errorData v1.SafeSyncErrorData
	require.NoError(t, proto.Unmarshal(errorWire, &errorData))
	require.Equal(t, "REVISION_CONFLICT", errorData.ErrorCode)
	require.Equal(t, int64(8), errorData.ActualRevision)
}

func TestResolveFileUploadChunkSize(t *testing.T) {
	t.Run("legacy upload uses configured size", func(t *testing.T) {
		got, err := resolveFileUploadChunkSize(0, 512*1024)
		require.NoError(t, err)
		require.Equal(t, int64(512*1024), got)
	})

	t.Run("safe upload honors requested size", func(t *testing.T) {
		got, err := resolveFileUploadChunkSize(1024*1024, 512*1024)
		require.NoError(t, err)
		require.Equal(t, int64(1024*1024), got)
	})

	for _, requested := range []int64{1, 16 * 1024 * 1024} {
		t.Run("rejects unsafe requested size", func(t *testing.T) {
			_, err := resolveFileUploadChunkSize(requested, 512*1024)
			require.Error(t, err)
		})
	}
}

func TestSafeSyncErrorCodesAndRBAC(t *testing.T) {
	tests := []struct {
		domainCode domain.SafeSyncErrorCode
		wireCode   int
	}{
		{domain.SafeSyncErrorUnsupported, 531},
		{domain.SafeSyncErrorBootstrapInProgress, 532},
		{domain.SafeSyncErrorStrictRequired, 533},
		{domain.SafeSyncErrorRevisionConflict, 534},
		{domain.SafeSyncErrorPathStateConflict, 535},
		{domain.SafeSyncErrorOperationIDReused, 536},
		{domain.SafeSyncErrorOperationExpired, 537},
		{domain.SafeSyncErrorRebootstrapRequired, 538},
		{domain.SafeSyncErrorBootstrapStateConflict, 539},
		{domain.SafeSyncErrorDeviceRoleConflict, 540},
		{domain.SafeSyncErrorDeviceReadOnly, 541},
	}
	for _, test := range tests {
		err := &domain.SafeSyncError{Code: test.domainCode, Message: "test"}
		wireCode, data := safeSyncErrorResponse(err)
		require.Equal(t, test.wireCode, wireCode.Code())
		require.Equal(t, string(test.domainCode), data.ErrorCode)
	}

	unknown, data := safeSyncErrorResponse(errors.New("database unavailable"))
	require.Same(t, code.ErrorServerInternal, unknown)
	require.Equal(t, "ERROR_300", data.ErrorCode)

	readActions := []string{SafeSyncReceiveStatus, SafeSyncReceiveBootstrapPage, SafeSyncReceiveEvents}
	for _, action := range readActions {
		require.Equal(t, []string{"note_r", "file_r"}, resolveRBACFunctions(action), action)
	}
	writeActions := []string{SafeSyncReceiveBootstrapStart, SafeSyncReceiveBootstrapCommit, SafeSyncReceiveBootstrapCancel, SafeSyncReceiveDeviceRoleRegister, SafeSyncReceiveNoteMutation, SafeSyncReceiveFolderMutation}
	for _, action := range writeActions {
		want := []string{"note_w", "file_w"}
		if action == SafeSyncReceiveNoteMutation || action == SafeSyncReceiveFolderMutation {
			want = []string{"note_w"}
		}
		require.Equal(t, want, resolveRBACFunctions(action), action)
	}
	for _, action := range []string{SafeSyncReceiveFileUploadStart, SafeSyncReceiveFileUploadCommit} {
		require.Equal(t, []string{"file_w"}, resolveRBACFunctions(action), action)
	}
}

func decodeSafeSyncResponseData(t *testing.T, action WebSocketSendAction, data any) []byte {
	t.Helper()
	wire, err := EnSendDTOToProtobuf(action, &pkgapp.Res{Code: 1, Status: true, Data: data})
	require.NoError(t, err)
	require.Greater(t, len(wire), 2)
	var envelope v1.WSMessage
	require.NoError(t, proto.Unmarshal(wire[2:], &envelope))
	var response v1.WSResponse
	require.NoError(t, proto.Unmarshal(envelope.Data, &response))
	return response.Data
}
