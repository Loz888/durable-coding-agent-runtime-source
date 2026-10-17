package repair17

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPre20GuardRejectsRenameWithoutAuthority(t *testing.T) {
	src, base := fixture(t, true)
	ctx := context.Background()
	w, err := Prepare(ctx, src, base, "guarded")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := Inspect(ctx, w.Path, "math.go")
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("lease expired before rename")
	_, err = ApplyGuarded(ctx, w.Path, Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a * b", New: "return a / b"},
		func(context.Context) error { return denied })
	if !errors.Is(err, denied) {
		t.Fatalf("guard rejection %v", err)
	}
	got, err := Inspect(ctx, w.Path, "math.go")
	if err != nil || got != sha {
		t.Fatalf("file changed after denied guard %s %v", got, err)
	}
}

func TestPre20WorktreeIdentityAndSymlinkRejection(t *testing.T) {
	src, base := fixture(t, true)
	ctx := context.Background()
	w, err := Prepare(ctx, src, base, "identity")
	if err != nil {
		t.Fatal(err)
	}
	if w.Identity == "" || w.GitDir == "" {
		t.Fatal("missing persistent identity")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(w.Path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenManaged(ctx, alias); err == nil {
		t.Fatal("symlink worktree accepted")
	}
	markerPath := filepath.Join(w.Path, markerFile)
	original, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(external, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, markerPath); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenManaged(ctx, w.Path); err == nil {
		t.Fatal("symlink marker accepted")
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.GitDir, "repair17-identity"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenManaged(ctx, w.Path); err == nil {
		t.Fatal("replaced Git identity accepted")
	}
}

func TestPre20MissingAndChangedBaselineRejected(t *testing.T) {
	src, base := fixture(t, true)
	ctx := context.Background()
	w, err := Prepare(ctx, src, base, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, w.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "changed baseline")
	if _, err := OpenManaged(ctx, w.Path); err == nil {
		t.Fatal("changed source commit accepted")
	}
	missing := filepath.Join(base, "missing")
	if _, err := OpenManaged(ctx, missing); err == nil {
		t.Fatal("missing worktree accepted")
	}
}
