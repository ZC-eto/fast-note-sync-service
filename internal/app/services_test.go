package app

import (
	"context"
	"errors"
	"testing"

	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRecoverPreparedSafeSyncOperations_FailsClosedForAnyUser(t *testing.T) {
	ctx := context.Background()
	userRepo := new(domainmocks.MockUserRepository)
	userRepo.On("GetAllUIDs", mock.Anything).Return([]int64{3, 7, 9}, nil).Once()
	recoveryErr := errors.New("prepared recovery failed")
	var recovered []int64

	err := recoverPreparedSafeSyncOperations(ctx, userRepo, func(_ context.Context, uid int64) error {
		recovered = append(recovered, uid)
		if uid == 7 {
			return recoveryErr
		}
		return nil
	})

	require.ErrorIs(t, err, recoveryErr)
	require.Equal(t, []int64{3, 7}, recovered)
	userRepo.AssertExpectations(t)
}
