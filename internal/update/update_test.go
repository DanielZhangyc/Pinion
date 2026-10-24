package update

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

func TestVersions(t *testing.T) {
	for _, input := range []string{"v1.2.3", "1.2.3", "1.2.3+build.9"} {
		got, err := version(input)
		if err != nil || got != "v1.2.3" {
			t.Fatalf("version(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "dev", "1", "v1", "1.2", "v1.2", "1.02.3", "1.2.3.4", "vv1.2.3", " 1.2.3"} {
		if _, err := version(input); err == nil {
			t.Errorf("accepted invalid version %q", input)
		}
	}
}

func TestReleaseURL(t *testing.T) {
	for _, value := range []string{
		"https://github.com/DanielZhangyc/Pinion/releases/tag/v1.2.3",
		"https://github.com/danielzhangyc/pinion/releases/tag/v1.2.3",
	} {
		if err := validateReleaseURL(value, Repository, "v1.2.3"); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{
		"http://github.com/DanielZhangyc/Pinion/releases/tag/v1.2.3",
		"https://github.com.evil.test/DanielZhangyc/Pinion/releases/tag/v1.2.3",
		"https://github.com/other/Pinion/releases/tag/v1.2.3",
		"https://github.com/DanielZhangyc/Pinion/releases/tag/v1.2.4",
		"https://github.com/DanielZhangyc/Pinion/releases/tag/V1.2.3",
		"https://github.com/DanielZhangyc/Pinion/releases/tag/v1.2.3?redirect=evil",
		"https://" + "user" + "@" + "github.com/DanielZhangyc/Pinion/releases/tag/v1.2.3",
		"https://github.com:443/DanielZhangyc/Pinion/releases/tag/v1.2.3",
		"https://github.com/DanielZhangyc/Pinion/releases",
	} {
		if err := validateReleaseURL(value, Repository, "v1.2.3"); err == nil {
			t.Errorf("accepted unrelated URL %q", value)
		}
	}
}

func TestInvalidInputAndCancellation(t *testing.T) {
	if _, err := Check(context.Background(), Repository, "dev"); err == nil {
		t.Fatal("accepted non-semantic current version")
	}
	if _, err := Check(context.Background(), "owner/repo/elsewhere", "1.0.0"); err == nil {
		t.Fatal("accepted invalid repository")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Check(ctx, Repository, "1.0.0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check: %v", err)
	}
}

// Uses genuine public GitHub responses, without local HTTP fixtures or tokens.
func TestGitHubIntegration(t *testing.T) {
	if os.Getenv("PINION_UPDATE_INTEGRATION") != "1" {
		t.Skip("set PINION_UPDATE_INTEGRATION=1 for real GitHub API checks")
	}
	ctx := context.Background()
	result, err := Check(ctx, "cli/cli", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Latest == nil || !result.Available {
		t.Fatalf("expected actual public newer stable release: %+v", result)
	}
	t.Logf("public release: %+v", *result.Latest)
	for _, current := range []string{result.Latest.Version, "999999.0.0"} {
		checked, err := Check(ctx, "cli/cli", current)
		if err != nil {
			t.Fatal(err)
		}
		if checked.Latest == nil || checked.Available {
			t.Fatalf("current %s: %+v", current, checked)
		}
		if semver.Prerelease("v"+checked.Latest.Version) != "" {
			t.Fatalf("reported prerelease: %+v", checked)
		}
	}
	target, err := Check(ctx, Repository, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if target.Latest == nil {
		if target.Available {
			t.Fatal("target without accessible release reported an available update")
		}
		t.Log("configured target: no accessible published release; not reported as up to date")
		return
	}
	latest, err := version(target.Latest.Version)
	if err != nil || semver.Prerelease(latest) != "" {
		t.Fatalf("target returned invalid stable version %q: %v", target.Latest.Version, err)
	}
	if target.Available != (semver.Compare(latest, "v0.1.0") > 0) {
		t.Fatalf("target update comparison disagrees with current 0.1.0: %+v", target)
	}
	releaseURL, err := url.Parse(target.Latest.URL)
	if err != nil {
		t.Fatal(err)
	}
	tag := releaseURL.Path[strings.LastIndex(releaseURL.Path, "/")+1:]
	tagVersion, err := version(tag)
	if err != nil || tagVersion != latest {
		t.Fatalf("target release URL tag %q disagrees with version %q: %v", tag, latest, err)
	}
	if err := validateReleaseURL(target.Latest.URL, Repository, tag); err != nil {
		t.Fatal(err)
	}
	t.Logf("configured target published release: %+v", *target.Latest)
}
