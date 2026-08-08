package fileurl

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	SourceGitHub = "github"

	// GitHubProbeURL is this fork's only release and update source.
	// GitHubProbeURL 是当前 fork 唯一的版本与更新源。
	GitHubProbeURL = "https://api.github.com/repos/ZC-eto/fast-note-sync-service/releases"

	defaultProbeTimeout = 3 * time.Second
	defaultCacheTTL     = 5 * time.Minute
)

// ProbeResult is the reachability and latency result of a source probe.
// ProbeResult 是单次源探测的可达性与延迟结果。
type ProbeResult struct {
	OK        bool
	LatencyMs int64
}

// ProbeSnapshot preserves the existing API shape while this fork uses GitHub only.
// CNB remains zero-valued so older compiled web clients can decode the response.
// ProbeSnapshot 保留旧 API 字段以兼容已编译前端；CNB 字段始终为空。
type ProbeSnapshot struct {
	GitHub    ProbeResult
	CNB       ProbeResult
	UseGitHub bool
	At        time.Time
}

// SourceSelector accepts legacy source settings but always selects this fork's GitHub releases.
// SourceSelector 接受旧配置值，但始终选择当前 fork 的 GitHub 发布源。
type SourceSelector struct {
	mu       sync.Mutex
	mode     string
	snapshot *ProbeSnapshot
	cacheTTL time.Duration
}

func NewSourceSelector(_ string) *SourceSelector {
	return &SourceSelector{mode: SourceGitHub, cacheTTL: defaultCacheTTL}
}

func (s *SourceSelector) SetMode(_ string) {
	s.mu.Lock()
	s.mode = SourceGitHub
	s.snapshot = nil
	s.mu.Unlock()
}

func (s *SourceSelector) IsGitHub() bool {
	return true
}

// Probe checks this fork's GitHub release endpoint and caches the result.
// Probe 只检查当前 fork 的 GitHub release 接口并缓存结果。
func (s *SourceSelector) Probe(ctx context.Context) ProbeSnapshot {
	snap := ProbeSnapshot{
		GitHub:    probeOne(ctx, GitHubProbeURL, ""),
		UseGitHub: true,
		At:        time.Now(),
	}
	s.mu.Lock()
	s.snapshot = &snap
	s.mu.Unlock()
	return snap
}

func (s *SourceSelector) Snapshot() *ProbeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil || time.Since(s.snapshot.At) > s.cacheTTL {
		return nil
	}
	snap := *s.snapshot
	return &snap
}

func (s *SourceSelector) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func probeOne(ctx context.Context, url, token string) ProbeResult {
	client := &http.Client{Timeout: defaultProbeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return ProbeResult{}
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return ProbeResult{LatencyMs: latency}
	}
	defer resp.Body.Close()
	return ProbeResult{OK: resp.StatusCode < http.StatusInternalServerError, LatencyMs: latency}
}
