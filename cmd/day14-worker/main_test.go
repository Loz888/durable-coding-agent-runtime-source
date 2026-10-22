package main

import (
	"path/filepath"
	"testing"
)

func TestCanonicalWorktreeBase(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, item := range []struct{ name, input, want string }{
		{"default-relative", "worktrees", filepath.Join(root, "worktrees")},
		{"normalized-relative", "./worktrees/../worktrees", filepath.Join(root, "worktrees")},
		{"explicit-absolute", filepath.Join(root, "fixed"), filepath.Join(root, "fixed")},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, e := canonicalWorktreeBase(item.input)
			if e != nil || got != item.want || !filepath.IsAbs(got) {
				t.Fatalf("base %q %v", got, e)
			}
		})
	}
	if _, e := canonicalWorktreeBase(""); e == nil {
		t.Fatal("empty base accepted")
	}
}
