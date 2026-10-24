package launch

import (
	"os"
	"sync"
	"testing"
)

func TestEmbeddedBootstrapDeployment(t *testing.T) {
	root := t.TempDir()
	path, err := deployEndfieldBootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := deployEndfieldBootstrap(root)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(again)
	if err != nil {
		t.Fatal(err)
	}
	if path != again || !os.SameFile(before, after) {
		t.Fatal("redeploying the same embedded helper replaced its file")
	}
	if err := os.Truncate(path, before.Size()-1); err != nil {
		t.Fatal(err)
	}
	if _, err := deployEndfieldBootstrap(root); err == nil {
		t.Fatal("a damaged deployed helper was accepted")
	}
	damaged, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if damaged.Size() != before.Size()-1 || !os.SameFile(before, damaged) {
		t.Fatal("deployment overwrote the existing damaged helper")
	}
}

func TestConcurrentBootstrapDeployment(t *testing.T) {
	root := t.TempDir()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			path, err := deployEndfieldBootstrap(root)
			if err != nil {
				t.Error(err)
				return
			}
			if err := verifyEndfieldBootstrap(path); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
}
