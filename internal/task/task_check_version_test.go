package task

import (
	"testing"

	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
)

func TestBuildLinksUsesSelfMaintainedFork(t *testing.T) {
	releases := []pkgapp.HistoricalVersion{{Version: "v3.6.1", ChangelogContent: "notes"}}
	link, changelog, content := buildLinks(releases, true, true)
	require.Equal(t, "https://github.com/ZC-eto/fast-note-sync-service/releases/tag/3.6.1", link)
	require.Equal(t, link, changelog)
	require.Equal(t, "notes", content)

	pluginLink, pluginChangelog, _ := buildLinks([]pkgapp.HistoricalVersion{{Version: "2.5.0"}}, false, true)
	require.Equal(t, "https://github.com/ZC-eto/obsidian-fast-note-sync/releases/tag/2.5.0", pluginLink)
	require.Equal(t, pluginLink, pluginChangelog)
}
