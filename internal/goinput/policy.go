// Package goinput plans fixed, offline Go test commands for bounded captured inputs.
// It only invokes trusted Go metadata editors; it never compiles or runs candidate code.
package goinput

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const SealedVendor = "sealed-vendor-v1"
const SealedWorkspace = "sealed-workspace-v1"

func ValidPolicy(policy string) bool {
	return policy == "" || policy == SealedVendor || policy == SealedWorkspace
}

// Mode extracts the input part of a persisted composite verification identity.
func Mode(identity string) string {
	for _, mode := range []string{SealedVendor, SealedWorkspace} {
		if strings.HasPrefix(identity, mode+":") {
			return mode
		}
	}
	return ""
}

// metadataEnv forbids toolchain downloads, ambient Go configuration and workspace
// discovery. The commands using it are mod/work edit -json, never go test/list/build.
func metadataEnv() []string {
	env := []string{"GOENV=off", "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0"}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "GOROOT", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type module struct {
	Module  struct{ Path string }
	Require []struct{ Path string }
	Replace []json.RawMessage
}

func readModule(ctx context.Context, path string) (module, error) {
	var mod module
	cmd := exec.CommandContext(ctx, "go", "mod", "edit", "-json")
	cmd.Dir, cmd.Env = path, metadataEnv()
	raw, err := cmd.Output()
	if err != nil || json.Unmarshal(raw, &mod) != nil {
		return mod, errors.New("verification requires valid local go.mod")
	}
	return mod, nil
}

// TestCommand accepts only an operator-selected mode. All returned arguments are
// generated here; the caller cannot supply flags, shell text, or external paths.
// Source links/special files and byte/file bounds are additionally enforced by the
// capturing caller. Running this planner does not establish an execution sandbox.
func TestCommand(ctx context.Context, path, policy string) ([]string, error) {
	if !ValidPolicy(policy) {
		return nil, errors.New("unknown sealed input policy")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("verification root must be absolute")
	}
	allowed := map[string]bool{".": true}
	if policy != SealedWorkspace {
		mod, err := readModule(ctx, path)
		if err != nil {
			return nil, err
		}
		if len(mod.Replace) > 0 {
			return nil, errors.New("verification forbids module replacements")
		}
		if policy == "" && len(mod.Require) > 0 {
			return nil, errors.New("verification currently supports stdlib-only modules without replace")
		}
		if policy == SealedVendor {
			st, err := os.Lstat(filepath.Join(path, "vendor", "modules.txt"))
			if err != nil || !st.Mode().IsRegular() {
				return nil, errors.New("sealed vendor requires committed vendor/modules.txt")
			}
		}
		if err := checkMembership(ctx, path, policy, allowed); err != nil {
			return nil, err
		}
		args := []string{"test"}
		if policy == SealedVendor {
			args = append(args, "-mod=vendor")
		}
		return append(args, "-count=1", "./..."), nil
	}
	cmd := exec.CommandContext(ctx, "go", "work", "edit", "-json", filepath.Join(path, "go.work"))
	cmd.Dir, cmd.Env = path, metadataEnv()
	raw, err := cmd.Output()
	if err != nil {
		return nil, errors.New("sealed workspace requires local go.work")
	}
	var work struct {
		Use     []struct{ DiskPath string }
		Replace []json.RawMessage
	}
	if json.Unmarshal(raw, &work) != nil || len(work.Use) < 1 || len(work.Use) > 8 || len(work.Replace) > 0 {
		return nil, errors.New("bounded workspace uses without replacements required")
	}
	allowed = map[string]bool{}
	modules := map[string]bool{}
	roots := []string{}
	for _, use := range work.Use {
		rel := filepath.Clean(use.DiskPath)
		if rel != "." && !filepath.IsLocal(rel) {
			return nil, errors.New("workspace module escapes captured root")
		}
		target := filepath.Join(path, rel)
		canonical, err := filepath.EvalSymlinks(target)
		if err != nil || canonical != target {
			return nil, errors.New("workspace module missing or linked")
		}
		if allowed[rel] {
			return nil, errors.New("duplicate workspace module")
		}
		allowed[rel] = true
		roots = append(roots, rel)
		mod, err := readModule(ctx, target)
		if err != nil {
			return nil, err
		}
		if len(mod.Replace) > 0 || mod.Module.Path == "" || modules[mod.Module.Path] {
			return nil, errors.New("workspace module identity/replacement refused")
		}
		modules[mod.Module.Path] = true
	}
	for _, rel := range roots {
		mod, err := readModule(ctx, filepath.Join(path, rel))
		if err != nil {
			return nil, err
		}
		for _, req := range mod.Require {
			if !modules[req.Path] {
				return nil, errors.New("workspace external dependencies must be sealed separately; unsupported")
			}
		}
	}
	if err := checkMembership(ctx, path, policy, allowed); err != nil {
		return nil, err
	}
	sort.Strings(roots)
	args := []string{"test", "-count=1"}
	for _, rel := range roots {
		if rel == "." {
			args = append(args, "./...")
		} else {
			args = append(args, "./"+filepath.ToSlash(rel)+"/...")
		}
	}
	return args, nil
}

func checkMembership(ctx context.Context, root, policy string, allowed map[string]bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("verification refuses symbolic links")
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() && entry.Name() == "vendor" && policy != SealedVendor {
			return errors.New("verification vendor input requires sealed-vendor policy")
		}
		if !entry.IsDir() && (entry.Name() == "go.work" || entry.Name() == "go.work.sum") && policy != SealedWorkspace {
			return errors.New("verification workspace input requires sealed-workspace policy")
		}
		if entry.Name() == "go.mod" && !allowed[filepath.Dir(rel)] {
			return errors.New("unlisted nested module")
		}
		return nil
	})
}
