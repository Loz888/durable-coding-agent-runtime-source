package repair17

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInterviewVerificationSnapshotBoundaries(t *testing.T) {
	// Reuse the public workspace fixture from this package.
	for _, kind := range []string{"snapshot", "symlink", "hardlink", "workspace", "replace", "dependency"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if e := os.Mkdir(source, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module fixture\ngo 1.23\n"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(source, "main.go"), []byte("package fixture\nconst A=1\n"), 0600); e != nil {
				t.Fatal(e)
			}
			git(t.Context(), source, "init", "-q")
			git(t.Context(), source, "add", ".")
			if _, e := git(t.Context(), source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "initial"); e != nil {
				t.Fatal(e)
			}
			w, e := Prepare(context.Background(), source, filepath.Join(root, "trees"), "input-"+kind)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "symlink":
				e = os.Symlink(filepath.Join(source, "main.go"), filepath.Join(w.Path, "link.go"))
			case "hardlink":
				e = os.Link(filepath.Join(w.Path, "main.go"), filepath.Join(w.Path, "link.go"))
			case "workspace":
				e = os.WriteFile(filepath.Join(w.Path, "go.work"), []byte("go 1.23\nuse .\n"), 0600)
			case "replace":
				e = os.WriteFile(filepath.Join(w.Path, "go.mod"), []byte("module fixture\ngo 1.23\nreplace example.com/x => ../external\n"), 0600)
			case "dependency":
				e = os.WriteFile(filepath.Join(w.Path, "go.mod"), []byte("module fixture\ngo 1.23\nrequire example.com/x v1.0.0\n"), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			v, e := CaptureVerification(context.Background(), w.Path, "unit-v1", time.Second)
			if kind != "snapshot" {
				if e == nil {
					v.Close()
					t.Fatal("unsupported input accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			defer v.Close()
			if e = os.WriteFile(filepath.Join(w.Path, "main.go"), []byte("package fixture\nconst A=2\n"), 0600); e != nil {
				t.Fatal(e)
			}
			b, _ := os.ReadFile(filepath.Join(v.Path, "main.go"))
			if string(b) != "package fixture\nconst A=1\n" {
				t.Fatal("source changed captured input")
			}
			if e = v.Unchanged(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}
