package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindWalksUp(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, StateDir), 0700)
	deep := filepath.Join(root, "a", "b")
	os.MkdirAll(deep, 0755)

	p, err := Find(deep)
	if err != nil || !sameDir(t, p.Root, root) {
		t.Fatalf("find: %+v %v", p, err)
	}
	if _, err := Open(deep); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open does not walk up: %v", err)
	}
	if _, err := Find(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("find outside a project: %v", err)
	}
	// A file named .assay is not a project.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, StateDir), nil, 0600)
	if _, err := Open(other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("file named .assay: %v", err)
	}
}

func TestEnsureIgnored(t *testing.T) {
	for _, tc := range []struct{ before, after string }{
		{"", "/.assay/\n"},
		{"node_modules", "node_modules\n/.assay/\n"},
		{"node_modules\n", "node_modules\n/.assay/\n"},
		{".assay\n", ".assay\n"},
		{"  /.assay/  \n", "  /.assay/  \n"},
	} {
		dir := t.TempDir()
		if tc.before != "" {
			os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(tc.before), 0644)
		}
		changed, err := EnsureIgnored(dir)
		got, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil || string(got) != tc.after || changed != (tc.before != tc.after) {
			t.Errorf("%q → %q (changed %v, %v)", tc.before, got, changed, err)
		}
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}
