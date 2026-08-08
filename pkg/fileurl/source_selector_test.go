package fileurl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSourceSelectorAlwaysUsesForkGitHub(t *testing.T) {
	for _, legacyMode := range []string{"auto", "github", "cnb", "invalid"} {
		s := NewSourceSelector(legacyMode)
		assert.True(t, s.IsGitHub())
		assert.Equal(t, SourceGitHub, s.Mode())
		s.SetMode(legacyMode)
		assert.True(t, s.IsGitHub())
		assert.Equal(t, SourceGitHub, s.Mode())
	}
}

func TestSourceSelectorSetModeInvalidatesSnapshot(t *testing.T) {
	s := NewSourceSelector("auto")
	s.snapshot = &ProbeSnapshot{UseGitHub: true, At: time.Now()}
	s.SetMode("cnb")
	assert.Nil(t, s.Snapshot())
	assert.True(t, s.IsGitHub())
}

func TestProbeOneReportsForkEndpointReachability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	result := probeOne(context.Background(), server.URL, "")
	assert.True(t, result.OK)
	assert.GreaterOrEqual(t, result.LatencyMs, int64(0))
}
