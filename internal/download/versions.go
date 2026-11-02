package download

import (
	"context"
	"strings"
)

// VersionInfo identifies the currently published official game package.
type VersionInfo struct {
	Version, ManifestMD5 string
}

// LatestVersion only queries metadata. It does not open an installation or
// download manifests, archives or game files.
func (m *Manager) LatestVersion(ctx context.Context, game GameID) (VersionInfo, error) {
	defer m.client.CloseIdleConnections()
	plan, err := m.latestPackage(ctx, game)
	if err != nil {
		return VersionInfo{}, err
	}
	return VersionInfo{Version: plan.Version, ManifestMD5: plan.ManifestMD5}, nil
}

// UpdateAvailable compares numeric version components and also detects a
// changed manifest for the same version. An older official version does not
// offer a downgrade as an update.
func (v VersionInfo) UpdateAvailable(installed Record) bool {
	latest, current := strings.Split(v.Version, "."), strings.Split(installed.Version, ".")
	for i := range max(len(latest), len(current)) {
		var a, b string
		if i < len(latest) {
			a = strings.TrimLeft(latest[i], "0")
		}
		if i < len(current) {
			b = strings.TrimLeft(current[i], "0")
		}
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		if a != b {
			return a > b
		}
	}
	return !strings.EqualFold(v.ManifestMD5, installed.ManifestMD5)
}
