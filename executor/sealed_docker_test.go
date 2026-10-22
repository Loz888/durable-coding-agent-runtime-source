//go:build linux && dockeraccept

package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/internal/goinput"
	"runtime-app/task"
	"strings"
	"testing"
	"time"
)

// This opt-in test requires a real local engine and operator-pinned image. A
// missing prerequisite is FAIL rather than SKIP; no image is pulled by the test.
func TestSealedInputsRealDocker(t *testing.T) {
	image := os.Getenv("RUNTIME_SANDBOX_IMAGE")
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	policy := GoSandbox{Image: image, Root: root, Timeout: 120 * time.Second, MaxBytes: 1 << 20, MaxFiles: 50}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	if err := policy.Probe(ctx); err != nil {
		t.Fatal("real Docker acceptance requires pinned local RUNTIME_SANDBOX_IMAGE", err)
	}
	for _, mode := range []string{goinput.SealedVendor, goinput.SealedWorkspace} {
		for _, wantPass := range []bool{true, false} {
			name := mode + "/pass"
			if !wantPass {
				name = mode + "/test-failure"
			}
			t.Run(name, func(t *testing.T) {
				source := t.TempDir()
				expected := "42"
				if !wantPass {
					expected = "43"
				}
				testCode := `package app
import("testing";"os";"syscall")
func TestSealedInput(t *testing.T){
 if os.Getuid()!=65532{t.Fatal("wrong container user")}
 if os.Getenv("GOPROXY")!="off"{t.Fatal("dependency network enabled")}
 var fs syscall.Statfs_t
 if err:=syscall.Statfs("/src",&fs);err!=nil||fs.Flags&1==0{t.Fatal("candidate mount is not read-only",err)}
 if os.WriteFile("/src/escape",[]byte("x"),0600)==nil{t.Fatal("candidate writable")}
 if Answer()!=` + expected + `{t.Fatal("semantic mismatch",Answer())}
}
`
				files := map[string]string{}
				if mode == goinput.SealedVendor {
					files["go.mod"] = "module example.invalid/app\ngo 1.23\nrequire example.invalid/tiny v1.0.0\n"
					files["main.go"] = "package app\nimport \"example.invalid/tiny\"\nfunc Answer()int{return tiny.Value}\n"
					files["main_test.go"] = testCode
					files["vendor/modules.txt"] = "# example.invalid/tiny v1.0.0\n## explicit; go 1.23\nexample.invalid/tiny\n"
					files["vendor/example.invalid/tiny/value.go"] = "package tiny\nconst Value=42\n"
				} else {
					files["go.work"] = "go 1.23\nuse (\n ./a\n ./b\n)\n"
					files["a/go.mod"] = "module example.invalid/app\ngo 1.23\nrequire example.invalid/tiny v0.0.0\n"
					files["a/main.go"] = "package app\nimport \"example.invalid/tiny\"\nfunc Answer()int{return tiny.Value}\n"
					files["a/main_test.go"] = testCode
					files["b/go.mod"] = "module example.invalid/tiny\ngo 1.23\n"
					files["b/value.go"] = "package tiny\nconst Value=42\n"
				}
				for name, data := range files {
					p := filepath.Join(source, name)
					if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				execution := policy
				execution.InputPolicy = mode
				out, err := execution.Execute(ctx, task.Task{Command: "go-test", Payload: source})
				if err != nil {
					t.Fatal("sandbox supervisor failed", err, out.Stdout)
				}
				if (out.ExitCode == 0) != wantPass {
					t.Fatalf("wrong actual verdict exit=%d: %s", out.ExitCode, out.Stdout)
				}
				if !wantPass && !strings.Contains(out.Stdout, "semantic mismatch") {
					t.Fatal("failure was not the intended assertion", out.Stdout)
				}
				var evidence struct {
					InputPolicy string   `json:"input_policy"`
					Command     []string `json:"command"`
					Tree        string   `json:"candidate_tree_sha256"`
				}
				if json.Unmarshal([]byte(out.Stderr), &evidence) != nil || evidence.InputPolicy != mode || len(evidence.Tree) != 64 || len(evidence.Command) < 4 {
					t.Fatal("missing sandbox execution evidence", out.Stderr)
				}
				if mode == goinput.SealedVendor && strings.Join(evidence.Command, " ") != "go test -mod=vendor -count=1 ./..." {
					t.Fatal("wrong vendor command", evidence.Command)
				}
				if mode == goinput.SealedWorkspace && strings.Join(evidence.Command, " ") != "go test -count=1 ./a/... ./b/..." {
					t.Fatal("wrong workspace command", evidence.Command)
				}
				for name, data := range files {
					raw, err := os.ReadFile(filepath.Join(source, name))
					if err != nil || string(raw) != data {
						t.Fatal("source fixture changed", name, err)
					}
				}
				raw, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=io.agent-runtime.root="+HashRoot(root)).Output()
				if err != nil || len(strings.Fields(string(raw))) != 0 {
					t.Fatal("owned sandbox containers remain", err)
				}
			})
		}
	}
}
