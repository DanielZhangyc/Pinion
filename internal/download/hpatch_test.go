package download

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pinion/internal/paths"
)

func TestHPatchDeploymentPreservesExistingHelper(t *testing.T) {
	root := t.TempDir()
	path, err := deployHPatchHelper(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := deployHPatchHelper(root)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(again)
	if err != nil || path != again || !os.SameFile(before, after) {
		t.Fatalf("deployment replaced existing helper: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := deployHPatchHelper(root); err == nil {
		t.Fatal("accepted helper without execute permission")
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{hpatchHelper[0] ^ 0xff}, 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := deployHPatchHelper(root); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("accepted corrupt helper: %v", err)
	}
}

func TestHPatchDeploymentRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path, err := deployHPatchHelper(root)
	if err != nil {
		t.Fatal(err)
	}
	target := path + ".original"
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := deployHPatchHelper(root); err == nil {
		t.Fatal("accepted symlink as helper")
	}
	contents, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(contents, hpatchHelper) {
		t.Fatalf("changed symlink target: %v", err)
	}
}

func TestHPatchInputAndOutputProtection(t *testing.T) {
	t.Setenv(paths.DataRootEnvironment, t.TempDir())
	// A real executable is an invalid patch; no game or patch data is fabricated.
	input, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	out := filepath.Join(root, "new")
	if err := applyHDiff(context.Background(), input, input, out, 0); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("invalid header accepted: %v", err)
	}
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created output before validating patch: %v", err)
	}
	contents, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, contents, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyHDiff(context.Background(), input, input, out, 0); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output accepted: %v", err)
	}
	after, err := os.Stat(out)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
		t.Fatalf("existing output changed: %v", err)
	}
	current, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(current, contents) {
		t.Fatalf("existing output contents changed: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(input, link); err != nil {
		t.Fatal(err)
	}
	if err := applyHDiff(context.Background(), input, input, link, 0); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("output symlink accepted: %v", err)
	}
	if err := applyHDiff(context.Background(), link, input, filepath.Join(root, "unused"), 0); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("input symlink accepted: %v", err)
	}
	if err := applyHDiff(context.Background(), root, input, filepath.Join(root, "unused"), 0); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("directory input accepted: %v", err)
	}
}

func TestHPatchCanceledContext(t *testing.T) {
	t.Setenv(paths.DataRootEnvironment, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(t.TempDir(), "output")
	if err := applyHDiff(ctx, "", "", output, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled operation created output: %v", err)
	}
}

// Explicit opt-in exercises an authentic CDN patch kept outside source control.
// It verifies the native parser and rejection before output creation; it does
// not claim successful synthesis without the matching official base file.
func TestHPatchOfficialSample(t *testing.T) {
	diff := os.Getenv("PINION_TEST_HDIFF_SAMPLE")
	if diff == "" {
		t.Skip("set PINION_TEST_HDIFF_SAMPLE to an authentic official HDIFFSF20 patch")
	}
	t.Setenv(paths.DataRootEnvironment, t.TempDir())
	helper, err := deployHPatchHelper(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info, err := exec.Command(helper, "--info", diff).CombinedOutput()
	if err != nil || !strings.Contains(string(info), "new=") || !strings.Contains(string(info), "old=") {
		t.Fatalf("cannot inspect real official sample: %s: %v", info, err)
	}
	var newSize, oldSize, step int64
	if _, err := fmt.Sscanf(string(info), "new=%d old=%d step=%d", &newSize, &oldSize, &step); err != nil {
		t.Fatal(err)
	}
	old, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "new")
	if err := applyHDiff(context.Background(), old, diff, out, newSize+1); err == nil || !strings.Contains(err.Error(), "manifest size") {
		t.Fatalf("accepted nonmatching manifest size: %v", err)
	}
	if err := applyHDiff(context.Background(), old, diff, out, newSize); err == nil || !strings.Contains(err.Error(), "old file size") {
		t.Fatalf("accepted nonmatching real old file: %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created output for wrong base: %v", err)
	}
}

// The optional inputs are genuine CDN files, kept outside source control.
// Their identities come from the official 78.0.0→79.0.0 patch map.
func TestHPatchOfficialSynthesis(t *testing.T) {
	base := os.Getenv("PINION_TEST_HDIFF_BASE")
	diff := os.Getenv("PINION_TEST_HDIFF_SAMPLE")
	if base == "" || diff == "" {
		t.Skip("set PINION_TEST_HDIFF_BASE and PINION_TEST_HDIFF_SAMPLE to the verified official samples")
	}
	t.Setenv(paths.DataRootEnvironment, t.TempDir())
	old, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 19323 || fmt.Sprintf("%x", md5.Sum(old)) != "0de4bf3d6a57568880d550286167228c" {
		t.Fatal("base does not match the official patch identity")
	}
	patch, err := os.ReadFile(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 4900 || fmt.Sprintf("%x", sha256.Sum256(patch)) != "005357b64451e9de1606c4b3d7f5cefeac9f81f643d2793105832b21c5518990" {
		t.Fatal("diff does not match the authenticated official sample")
	}
	out := filepath.Join(t.TempDir(), "char_1027_greyy2.ab")
	if err := applyHDiff(t.Context(), base, diff, out, 19520); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 19520 || fmt.Sprintf("%x", md5.Sum(result)) != "15ac206da68a03971eb6a742b431defc" {
		t.Fatal("synthesized file does not match the official target")
	}
	after, err := os.ReadFile(base)
	if err != nil || !bytes.Equal(after, old) {
		t.Fatal("HDiff changed the original official base")
	}
	t.Log("official HDiff synthesis: 19,323-byte base + 4,900-byte patch → 19,520-byte target; target MD5 verified, base unchanged")
}
