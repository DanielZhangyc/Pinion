// Package update checks public stable GitHub releases without installing them.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const Repository = "DanielZhangyc/Pinion"

const maxResponseBytes = 2 << 20

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)

type Release struct {
	Version string
	URL     string
}

type Result struct {
	// Latest is nil when GitHub reports no accessible published release (404).
	Latest    *Release
	Available bool
}

// Check compares currentVersion with the repository's latest stable release.
// A missing/inaccessible release is distinct from an up-to-date installation.
func Check(ctx context.Context, repository, currentVersion string) (Result, error) {
	if !repositoryPattern.MatchString(repository) {
		return Result{}, fmt.Errorf("invalid GitHub repository %q", repository)
	}
	current, err := version(currentVersion)
	if err != nil {
		return Result{}, fmt.Errorf("current version: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/releases/latest", nil)
	if err != nil {
		return Result{}, fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Pinion/"+currentVersion)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("check GitHub release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Result{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("GitHub release check returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read GitHub release: %w", err)
	}
	if len(body) > maxResponseBytes {
		return Result{}, fmt.Errorf("GitHub release response exceeds %d bytes", maxResponseBytes)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return Result{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	latest, err := version(release.Tag)
	if err != nil {
		return Result{}, fmt.Errorf("release tag: %w", err)
	}
	if release.Draft || release.Prerelease || semver.Prerelease(latest) != "" {
		return Result{}, fmt.Errorf("GitHub latest release is not a published stable release")
	}
	if err := validateReleaseURL(release.URL, repository, release.Tag); err != nil {
		return Result{}, err
	}
	return Result{
		Latest:    &Release{Version: strings.TrimPrefix(latest, "v"), URL: release.URL},
		Available: semver.Compare(latest, current) > 0,
	}, nil
}

func version(value string) (string, error) {
	normalized := "v" + strings.TrimPrefix(value, "v")
	if !semver.IsValid(normalized) || semver.Canonical(normalized)+semver.Build(normalized) != normalized {
		return "", fmt.Errorf("invalid semantic version %q", value)
	}
	return semver.Canonical(normalized), nil
}

func validateReleaseURL(value, repository, tag string) error {
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid release URL: %w", err)
	}
	prefix := "/" + repository + "/releases/tag/"
	if u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		len(u.Path) < len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) || u.Path[len(prefix):] != tag {
		return fmt.Errorf("release URL does not belong to %s release %q", repository, tag)
	}
	return nil
}
