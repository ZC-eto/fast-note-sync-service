package task

import (
	"errors"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/require"
)

func TestShouldSkipLegacyFIDSync(t *testing.T) {
	tests := []struct {
		name   string
		status *dto.SafeSyncStatusResponse
		err    error
		skip   bool
	}{
		{name: "legacy off vault", status: &dto.SafeSyncStatusResponse{Capability: true, State: "OFF"}},
		{name: "strict vault", status: &dto.SafeSyncStatusResponse{Capability: true, State: "STRICT"}, skip: true},
		{name: "bootstrap vault", status: &dto.SafeSyncStatusResponse{Capability: true, State: "BOOTSTRAPPING"}, skip: true},
		{name: "unsupported backend continues legacy maintenance", status: &dto.SafeSyncStatusResponse{Capability: false, State: "OFF"}, err: errors.New("unsupported")},
		{name: "indeterminate state fails closed", err: errors.New("database unavailable"), skip: true},
		{name: "missing status fails closed", skip: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.skip, shouldSkipLegacyFIDSync(test.status, test.err))
		})
	}
}
