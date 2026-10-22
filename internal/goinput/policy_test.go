package goinput

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func workspaceFiles() map[string]string {
	return map[string]string{
		"go.work":  "go 1.23\nuse (\n ./b\n ./a\n)\n",
		"a/go.mod": "module example.invalid/a\ngo 1.23\nrequire example.invalid/b v0.0.0\n",
		"b/go.mod": "module example.invalid/b\ngo 1.23\n",
	}
}
func vendorFiles() map[string]string {
	return map[string]string{
		"go.mod":                               "module example.invalid/app\ngo 1.23\nrequire example.invalid/tiny v1.0.0\n",
		"vendor/modules.txt":                   "# example.invalid/tiny v1.0.0\n## explicit; go 1.23\nexample.invalid/tiny\n",
		"vendor/example.invalid/tiny/value.go": "package tiny\nconst Value=42\n",
	}
}
func TestPlansFixedCommandsWithoutRunningCandidate(t *testing.T) {
	cases := []struct {
		name, policy string
		files        map[string]string
		want         []string
	}{
		{"standalone", "", map[string]string{"go.mod": "module example.invalid/app\ngo 1.23\n", "broken.go": "this deliberately is not compilable Go"}, []string{"test", "-count=1", "./..."}},
		{"vendor", SealedVendor, vendorFiles(), []string{"test", "-mod=vendor", "-count=1", "./..."}},
		{"workspace", SealedWorkspace, workspaceFiles(), []string{"test", "-count=1", "./a/...", "./b/..."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := TestCommand(context.Background(), fixture(t, c.files), c.policy)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}
func TestRejectsInputOutsideSelectedContract(t *testing.T) {
	cases := []struct {
		name, policy string
		files        func() map[string]string
		change       func(map[string]string)
	}{
		{"unknown-mode", SealedVendor + ":caller-flags", vendorFiles, nil},
		{"default-external", "", vendorFiles, nil},
		{"default-workspace", "", func() map[string]string {
			return map[string]string{"go.mod": "module example.invalid/app\ngo 1.23\n", "go.work": "go 1.23\nuse .\n"}
		}, nil},
		{"default-vendor", "", func() map[string]string {
			return map[string]string{"go.mod": "module example.invalid/app\ngo 1.23\n", "vendor/anything": "opaque"}
		}, nil},
		{"vendor-replace", SealedVendor, vendorFiles, func(f map[string]string) { f["go.mod"] += "replace example.invalid/tiny => ../outside\n" }},
		{"vendor-missing-manifest", SealedVendor, vendorFiles, func(f map[string]string) { delete(f, "vendor/modules.txt") }},
		{"vendor-nested-module", SealedVendor, vendorFiles, func(f map[string]string) { f["nested/go.mod"] = "module example.invalid/hidden\ngo 1.23\n" }},
		{"vendor-workspace", SealedVendor, vendorFiles, func(f map[string]string) { f["go.work"] = "go 1.23\nuse .\n" }},
		{"workspace-escape", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["go.work"] = "go 1.23\nuse ../outside\n" }},
		{"workspace-absolute", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["go.work"] = "go 1.23\nuse /tmp/outside\n" }},
		{"workspace-replace", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["go.work"] += "replace example.invalid/b => ./b\n" }},
		{"workspace-module-replace", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["a/go.mod"] += "replace example.invalid/b => ../b\n" }},
		{"workspace-external-require", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["b/go.mod"] += "require example.invalid/unsealed v1.0.0\n" }},
		{"workspace-unlisted-module", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["hidden/go.mod"] = "module example.invalid/hidden\ngo 1.23\n" }},
		{"workspace-duplicate-identity", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["b/go.mod"] = "module example.invalid/a\ngo 1.23\n" }},
		{"workspace-vendor", SealedWorkspace, workspaceFiles, func(f map[string]string) { f["vendor/anything"] = "opaque" }},
		{"workspace-module-bound", SealedWorkspace, workspaceFiles, func(f map[string]string) {
			f["go.work"] = "go 1.23\nuse (\n ./a\n ./b\n ./c\n ./d\n ./e\n ./f\n ./g\n ./h\n ./i\n)\n"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.files()
			if c.change != nil {
				c.change(f)
			}
			if args, err := TestCommand(context.Background(), fixture(t, f), c.policy); err == nil {
				t.Fatalf("out-of-contract input planned: %q", args)
			}
		})
	}
}
func TestWorkspaceModuleAndOtherSymlinksRejected(t *testing.T) {
	for _, moduleLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-link", true: "module-link"}[moduleLink], func(t *testing.T) {
			root := fixture(t, workspaceFiles())
			target := filepath.Join(root, "linked")
			if moduleLink {
				target = filepath.Join(root, "b")
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(t.TempDir(), target); err != nil {
				t.Fatal(err)
			}
			if _, err := TestCommand(context.Background(), root, SealedWorkspace); err == nil {
				t.Fatal("linked input accepted")
			}
		})
	}
}

func TestMetadataPlannerIgnoresAmbientGoConfiguration(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "goenv")
	if err := os.WriteFile(envFile, []byte("GOWORK=/outside/missing.work\nGOTOOLCHAIN=go1.99.0\nGOFLAGS=-unsupported-ambient-flag\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", envFile)
	t.Setenv("GOWORK", "/outside/missing.work")
	t.Setenv("GOTOOLCHAIN", "go1.99.0")
	t.Setenv("GOFLAGS", "-unsupported-ambient-flag")
	if args, err := TestCommand(context.Background(), fixture(t, workspaceFiles()), SealedWorkspace); err != nil || len(args) != 4 {
		t.Fatal("ambient config reached metadata parser", args, err)
	}
}
