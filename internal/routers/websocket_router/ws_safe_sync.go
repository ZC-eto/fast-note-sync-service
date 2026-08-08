package websocket_router

import (
	"errors"
	"fmt"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
)

type SafeSyncWSHandler struct {
	*WSHandler
	fileHandler *FileWSHandler
}

func NewSafeSyncWSHandler(a *app.App) *SafeSyncWSHandler {
	return &SafeSyncWSHandler{WSHandler: NewWSHandler(a), fileHandler: NewFileWSHandler(a)}
}

func (h *SafeSyncWSHandler) Status(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeSyncStatusRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, "")
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.Status(c.Context(), c.User.UID, vaultID)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, "", "websocket_router.safe_sync.Status")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault), SafeSyncStatusAck)
}

func (h *SafeSyncWSHandler) BootstrapStart(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeSyncBootstrapStartRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.BootstrapStart(c.Context(), c.User.UID, vaultID, params.DeviceID)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.BootstrapStart")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncBootstrapStartAck)
}

func (h *SafeSyncWSHandler) BootstrapPage(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeSyncBootstrapPageRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.BootstrapPage(c.Context(), c.User.UID, vaultID, params.SessionID, params.Cursor, int(params.PageSize))
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.BootstrapPage")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncBootstrapPageAck)
}

func (h *SafeSyncWSHandler) BootstrapCommit(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeSyncBootstrapCommitRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.BootstrapCommit(c.Context(), c.User.UID, vaultID, params.SessionID, params.ManifestHash, params.SnapshotVaultRevision)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.BootstrapCommit")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncBootstrapCommitAck)
}

func (h *SafeSyncWSHandler) BootstrapCancel(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeSyncBootstrapCancelRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.BootstrapCancel(c.Context(), c.User.UID, vaultID, params.SessionID)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.BootstrapCancel")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncBootstrapCancelAck)
}

func (h *SafeSyncWSHandler) Events(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeSyncEventsRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.Events(c.Context(), c.User.UID, vaultID, params.AfterRevision, int(params.PageSize))
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.Events")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncEventsAck)
}

func (h *SafeSyncWSHandler) DeviceRoleRegister(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.DeviceRoleRegisterRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeSyncService.RegisterDeviceRole(c.Context(), c.User.UID, vaultID, params.DeviceID, domain.DeviceSyncRole(params.Role))
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.DeviceRoleRegister")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncDeviceRoleStatusAck)
}

func (h *SafeSyncWSHandler) NoteMutation(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	h.mutate(c, msg, domain.SyncResourceTypeNote, SafeSyncNoteMutationAck)
}

func (h *SafeSyncWSHandler) FolderMutation(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	h.mutate(c, msg, domain.SyncResourceTypeFolder, SafeSyncFolderMutationAck)
}

func (h *SafeSyncWSHandler) FileMutation(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	h.mutate(c, msg, domain.SyncResourceTypeFile, SafeSyncFileMutationAck)
}

func (h *SafeSyncWSHandler) FileUploadStart(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeFileUploadStartRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	if err := h.App.SafeMutationCoordinator.ValidateFileUpload(c.Context(), c.User.UID, vaultID, &params.SafeMutationRequest); err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.FileUploadStart.Validate")
		return
	}
	session, err := h.fileHandler.handleFileUploadSessionCreate(
		c, params.Vault, params.Path, params.PathHash, params.ContentHash,
		params.Size, params.Ctime, params.Mtime, params.Context, params.ChunkSize, &params.SafeMutationRequest,
	)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, code.ErrorFileUploadFailed, params.Vault, params.Context, "websocket_router.safe_sync.FileUploadStart.Session")
		return
	}
	c.ToResponse(code.Success.WithData(dto.SafeFileUploadStartResponse{
		SessionID: session.ID, NextChunkIndex: session.nextChunkIndex(),
		OperationID: params.OperationID, ExpiresAt: session.ExpiresAt.UnixMilli(),
	}).WithVault(params.Vault).WithContext(params.Context), SafeSyncFileUploadStartAck)
}

func (h *SafeSyncWSHandler) FileUploadCommit(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SafeFileUploadCommitRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	binarySession := c.Server.GetSession(c.User.ID, params.SessionID)
	session, ok := binarySession.(*FileUploadBinaryChunkSession)
	if !ok || session == nil {
		h.respondSafeSyncError(c, msg, code.ErrorFileUploadSessionNotFound, nil, params.Vault, params.Context, "websocket_router.safe_sync.FileUploadCommit.Session")
		return
	}
	session.mu.Lock()
	ready := session.SafeUpload && session.UploadReady && session.SafeMutation != nil
	mutation := session.SafeMutation
	savePath := session.SavePath
	identityMatches := ready && mutation.DeviceID == params.DeviceID && mutation.OperationID == params.OperationID &&
		session.Vault == params.Vault && session.ContentHash == params.ContentHash && session.Size == params.Size
	session.mu.Unlock()
	if !identityMatches {
		h.respondSafeSyncError(c, msg, &domain.SafeSyncError{Code: domain.SafeSyncErrorOperationIDReused, Message: "file upload session does not match commit"}, nil, params.Vault, params.Context, "websocket_router.safe_sync.FileUploadCommit.Identity")
		return
	}
	result, err := h.App.SafeMutationCoordinator.CommitFile(c.Context(), c.User.UID, vaultID, mutation, savePath)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.FileUploadCommit")
		return
	}
	session.mu.Lock()
	session.isCompleted = true
	session.mu.Unlock()
	c.Server.RemoveSession(c.User.ID, session.ID)
	session.Cleanup()
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), SafeSyncFileUploadCommitAck)
	if !result.Replayed {
		h.broadcastSafeSyncEvent(c, c.User.UID, vaultID, params.Vault, result.VaultRevision)
	}
}

func (h *SafeSyncWSHandler) mutate(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, resourceType domain.SyncResourceType, responseAction WebSocketSendAction) {
	params := &dto.SafeMutationRequest{}
	if !h.bindSafeSyncRequest(c, msg, params) {
		return
	}
	vaultID, ok := h.resolveSafeSyncVaultID(c, msg, params.Vault, params.Context)
	if !ok {
		return
	}
	result, err := h.App.SafeMutationCoordinator.Mutate(c.Context(), c.User.UID, vaultID, resourceType, params)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, params.Vault, params.Context, "websocket_router.safe_sync.Mutate")
		return
	}
	c.ToResponse(code.Success.WithData(result).WithVault(params.Vault).WithContext(params.Context), responseAction)
	if !result.Replayed {
		h.broadcastSafeSyncEvent(c, c.User.UID, vaultID, params.Vault, result.VaultRevision)
	}
}

func (h *SafeSyncWSHandler) broadcastSafeSyncEvent(c *pkgapp.WebsocketClient, uid, vaultID int64, vault string, vaultRevision int64) {
	result, err := h.App.SafeSyncService.Events(c.Context(), uid, vaultID, vaultRevision-1, 1)
	if err != nil {
		h.logError(c, "websocket_router.safe_sync.BroadcastEvent", err)
		return
	}
	if len(result.Events) != 1 || result.Events[0].VaultRevision != vaultRevision {
		h.logError(c, "websocket_router.safe_sync.BroadcastEvent", errors.New("committed safe sync event is unavailable"))
		return
	}
	c.BroadcastResponse(code.Success.WithData(result.Events[0]).WithVault(vault), true, SafeSyncEvent)
}

func (h *SafeSyncWSHandler) bindSafeSyncRequest(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, params any) bool {
	valid, validationErrors := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if valid {
		return true
	}
	err := errors.New(validationErrors.ErrorsToString())
	h.respondSafeSyncError(c, msg, err, code.ErrorInvalidParams, "", "", "websocket_router.safe_sync.BindAndValid")
	return false
}

func (h *SafeSyncWSHandler) resolveSafeSyncVaultID(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, vault, requestContext string) (int64, bool) {
	vaultID, err := h.App.VaultService.MustGetID(c.Context(), c.User.UID, vault)
	if err != nil {
		h.respondSafeSyncError(c, msg, err, nil, vault, requestContext, "websocket_router.safe_sync.ResolveVault")
		return 0, false
	}
	return vaultID, true
}

func (h *SafeSyncWSHandler) respondSafeSyncError(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, err error, fallback *code.Code, vault, requestContext, method string) {
	h.logError(c, method, err)
	responseCode, data := safeSyncErrorResponse(err)
	if fallback != nil {
		var safeErr *domain.SafeSyncError
		var codeErr *code.Code
		if !errors.As(err, &safeErr) && !errors.As(err, &codeErr) {
			responseCode = fallback
			data.ErrorCode = fmt.Sprintf("ERROR_%d", fallback.Code())
		}
	}
	response := responseCode.WithDetails(err.Error()).WithData(data)
	if vault != "" {
		response = response.WithVault(vault)
	}
	if requestContext != "" {
		response = response.WithContext(requestContext)
	}
	c.ToResponse(response, safeSyncResponseAction(msg.Type))
}

func safeSyncErrorResponse(err error) (*code.Code, dto.SafeSyncErrorData) {
	var safeErr *domain.SafeSyncError
	if errors.As(err, &safeErr) {
		return safeSyncCode(safeErr.Code), dto.SafeSyncErrorData{ErrorCode: string(safeErr.Code)}
	}
	var codeErr *code.Code
	if errors.As(err, &codeErr) {
		return codeErr, dto.SafeSyncErrorData{ErrorCode: fmt.Sprintf("ERROR_%d", codeErr.Code())}
	}
	return code.ErrorServerInternal, dto.SafeSyncErrorData{ErrorCode: fmt.Sprintf("ERROR_%d", code.ErrorServerInternal.Code())}
}

func safeSyncCode(errorCode domain.SafeSyncErrorCode) *code.Code {
	switch errorCode {
	case domain.SafeSyncErrorUnsupported:
		return code.ErrorSafeSyncUnsupported
	case domain.SafeSyncErrorBootstrapInProgress:
		return code.ErrorSafeSyncBootstrapProgress
	case domain.SafeSyncErrorStrictRequired:
		return code.ErrorSafeSyncStrictRequired
	case domain.SafeSyncErrorRevisionConflict:
		return code.ErrorSafeSyncRevisionConflict
	case domain.SafeSyncErrorPathStateConflict:
		return code.ErrorSafeSyncPathConflict
	case domain.SafeSyncErrorOperationIDReused:
		return code.ErrorSafeSyncOperationIDReused
	case domain.SafeSyncErrorOperationExpired:
		return code.ErrorSafeSyncOperationExpired
	case domain.SafeSyncErrorRebootstrapRequired:
		return code.ErrorSafeSyncRebootstrap
	case domain.SafeSyncErrorBootstrapStateConflict:
		return code.ErrorSafeSyncBootstrapConflict
	case domain.SafeSyncErrorDeviceRoleConflict:
		return code.ErrorSafeSyncDeviceRoleConflict
	case domain.SafeSyncErrorDeviceReadOnly:
		return code.ErrorSafeSyncDeviceReadOnly
	default:
		return code.ErrorServerInternal
	}
}

func safeSyncResponseAction(requestAction string) WebSocketSendAction {
	switch requestAction {
	case SafeSyncReceiveStatus:
		return SafeSyncStatusAck
	case SafeSyncReceiveBootstrapStart:
		return SafeSyncBootstrapStartAck
	case SafeSyncReceiveBootstrapPage:
		return SafeSyncBootstrapPageAck
	case SafeSyncReceiveBootstrapCommit:
		return SafeSyncBootstrapCommitAck
	case SafeSyncReceiveBootstrapCancel:
		return SafeSyncBootstrapCancelAck
	case SafeSyncReceiveEvents:
		return SafeSyncEventsAck
	case SafeSyncReceiveNoteMutation:
		return SafeSyncNoteMutationAck
	case SafeSyncReceiveFolderMutation:
		return SafeSyncFolderMutationAck
	case SafeSyncReceiveFileMutation:
		return SafeSyncFileMutationAck
	case SafeSyncReceiveFileUploadStart:
		return SafeSyncFileUploadStartAck
	case SafeSyncReceiveFileUploadCommit:
		return SafeSyncFileUploadCommitAck
	case SafeSyncReceiveDeviceRoleRegister:
		return SafeSyncDeviceRoleStatusAck
	default:
		return ""
	}
}
