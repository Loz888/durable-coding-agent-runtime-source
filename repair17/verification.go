package repair17

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// VerificationInput is a bounded copy of a managed candidate, not a security sandbox.
// The default contract is standalone stdlib-only modules. Explicit sealed policies
// permit bounded vendored dependencies or root-contained workspaces, without replace.
// Links and ambient files are never part of this verification contract.
// Trusted tests must not depend on external data/services or modify the toolchain.
type VerificationInput struct {
	Path     string
	Digest   string
	metadata []byte
}

func (v *VerificationInput) Close() {
	if v != nil && v.Path != "" {
		_ = os.RemoveAll(v.Path)
	}
}
func (v *VerificationInput) Verify(ctx context.Context, timeout time.Duration) (TestResult, error) {
	if v == nil || v.Path == "" {
		return TestResult{}, errors.New("missing captured verification input")
	}
	var meta struct{ Policy string }
	if json.Unmarshal(v.metadata, &meta) != nil {
		return TestResult{}, errors.New("missing verification policy")
	}
	args, e := sealedCommand(ctx, v.Path, meta.Policy)
	if e != nil {
		return TestResult{}, e
	}
	return runTestCommand(ctx, v.Path, timeout, args, verificationEnv(v.Path, meta.Policy))
}
func (v *VerificationInput) Unchanged(ctx context.Context) error {
	got, err := verificationTree(ctx, v.Path, "", v.metadata)
	if err != nil {
		return err
	}
	if got != v.Digest {
		return errors.New("verification snapshot changed during tests")
	}
	return nil
}

func verificationMetadata(ctx context.Context, w Workspace, policy string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 || timeout > 5*time.Minute {
		return nil, errors.New("invalid verification timeout")
	}
	binary, err := exec.LookPath("go")
	if err != nil {
		return nil, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		return nil, err
	}
	binaryHash := sha256.Sum256(raw)
	cmd := exec.CommandContext(ctx, binary, "version")
	cmd.Env = trustedGoTestEnv()
	version, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// Include every forwarded setting. Secrets are never forwarded; only the digest is persisted.
	args, e := sealedCommand(ctx, w.Path, policy)
	if e != nil {
		return nil, e
	}
	environment := trustedGoTestEnv()
	if inputMode(policy) == SealedWorkspace {
		environment = append(environment, "workspace=CAPTURE/go.work")
	}
	return json.Marshal(struct {
		Version, Identity, Commit, Policy, Toolchain, Binary string
		Command                                              []string
		Environment                                          []string
		Timeout                                              int64
	}{
		"verification-v1", w.Identity, w.Commit, policy, string(version), hex.EncodeToString(binaryHash[:]),
		append([]string{"go"}, args...), environment, int64(timeout)})
}

func verificationTree(ctx context.Context, root, destination string, metadata []byte) (string, error) {
	var meta struct{ Policy string }
	_ = json.Unmarshal(metadata, &meta)
	mode := inputMode(meta.Policy)
	hash := sha256.New()
	hash.Write(metadata)
	count := 0
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == markerFile {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("verification refuses symbolic links")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if destination != "" {
				return os.MkdirAll(filepath.Join(destination, rel), 0700)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("verification refuses special files")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
			return errors.New("verification refuses hard links")
		}
		if (mode != SealedWorkspace && (entry.Name() == "go.work" || entry.Name() == "go.work.sum" || (entry.Name() == "go.mod" && rel != "go.mod"))) || (mode != SealedVendor && strings.HasPrefix(rel, "vendor"+string(filepath.Separator))) {
			return errors.New("verification requires standalone module without workspace/vendor")
		}
		count++
		total += info.Size()
		if count > 10000 || total > 64<<20 {
			return errors.New("verification input exceeds bound")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "\x00%s\x00%d\x00%d\x00", filepath.ToSlash(rel), info.Mode().Perm(), len(data))
		hash.Write(data)
		if destination != "" {
			if err = os.WriteFile(filepath.Join(destination, rel), data, info.Mode().Perm()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "v1:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// CaptureVerification copies all candidate files (not just the changed Go file),
// checks a second source read and validates the copy. Tests run only in this copy.
// Subsequent source changes cannot change what the test process reads.
func CaptureVerification(ctx context.Context, path, policy string, timeout time.Duration) (*VerificationInput, error) {
	w, err := OpenManaged(ctx, path)
	if err != nil {
		return nil, err
	}
	metadata, err := verificationMetadata(ctx, w, policy, timeout)
	if err != nil {
		return nil, err
	}
	temp, err := os.MkdirTemp("", "runtime-verification-")
	if err != nil {
		return nil, err
	}
	v := &VerificationInput{Path: temp, metadata: metadata}
	ok := false
	defer func() {
		if !ok {
			v.Close()
		}
	}()
	v.Digest, err = verificationTree(ctx, w.Path, temp, metadata)
	if err != nil {
		return nil, err
	}
	source, err := verificationTree(ctx, w.Path, "", metadata)
	if err != nil {
		return nil, err
	}
	if source != v.Digest {
		return nil, errors.New("candidate changed while capturing verification")
	}
	if err = v.Unchanged(ctx); err != nil {
		return nil, err
	}
	if _, err = sealedCommand(ctx, temp, policy); err != nil {
		return nil, err
	}
	ok = true
	return v, nil
}

func VerificationDigest(ctx context.Context, path, policy string, timeout time.Duration) (string, error) {
	w, err := OpenManaged(ctx, path)
	if err != nil {
		return "", err
	}
	metadata, err := verificationMetadata(ctx, w, policy, timeout)
	if err != nil {
		return "", err
	}
	if _, err = sealedCommand(ctx, path, policy); err != nil {
		return "", err
	}
	return verificationTree(ctx, path, "", metadata)
}
