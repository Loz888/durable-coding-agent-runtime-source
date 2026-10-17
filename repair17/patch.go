package repair17

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxSourceBytes = 1024 * 1024

type Patch struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Old            string `json:"old"`
	New            string `json:"new"`
}

type PatchResult struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Only existing ordinary non-test Go files are allowed.
func resolveSource(
	root string,
	rel string,
) (string, os.FileInfo, error) {
	if !filepath.IsLocal(rel) ||
		filepath.IsAbs(rel) ||
		filepath.Ext(rel) != ".go" ||
		strings.HasSuffix(rel, "_test.go") {
		return "", nil, errors.New(
			"only local non-test .go paths may be patched",
		)
	}

	parts := strings.Split(
		filepath.Clean(rel),
		string(filepath.Separator),
	)

	current := root

	for i, part := range parts {
		if part == ".git" ||
			strings.HasPrefix(part, ".") ||
			part == "vendor" {
			return "", nil, errors.New(
				"protected path component",
			)
		}

		current = filepath.Join(current, part)

		st, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}

		if st.Mode()&os.ModeSymlink != 0 {
			return "", nil, errors.New(
				"symlinks are not writable",
			)
		}

		if i < len(parts)-1 && !st.IsDir() {
			return "", nil, errors.New(
				"non-directory in path",
			)
		}

		if i == len(parts)-1 {
			if !st.Mode().IsRegular() ||
				st.Size() > maxSourceBytes {
				return "", nil, errors.New(
					"target must be a regular file <= 1 MiB",
				)
			}

			return current, st, nil
		}
	}

	return "", nil, errors.New(
		"invalid source path",
	)
}

func Inspect(
	ctx context.Context,
	worktree string,
	rel string,
) (string, error) {
	w, err := OpenManaged(ctx, worktree)
	if err != nil {
		return "", err
	}

	target, _, err := resolveSource(
		w.Path, rel,
	)
	if err != nil {
		return "", err
	}

	b, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}

	return digest(b), nil
}

// Stale patches are rejected rather than replayed.
// One writer per worktree is required.
func Apply(ctx context.Context, worktree string, p Patch) (PatchResult, error) {
	return ApplyGuarded(ctx, worktree, p, nil)
}

// ApplyGuarded checks ownership immediately before the atomic rename.
// The caller must serialize authorized writers; this is not an OS sandbox.
func ApplyGuarded(ctx context.Context, worktree string, p Patch, guard func(context.Context) error) (PatchResult, error) {
	var result PatchResult

	w, err := OpenManaged(ctx, worktree)
	if err != nil {
		return result, err
	}

	if guard == nil && w.GitDir != "" {
		_, markerErr := os.Lstat(filepath.Join(w.GitDir, "repair19-bound"))
		if markerErr == nil {
			return result, errors.New("durable-bound worktree requires the guarded repair19.Store write path")
		}
		if !os.IsNotExist(markerErr) {
			return result, markerErr
		}
	}
	target, st, err := resolveSource(
		w.Path, p.Path,
	)
	if err != nil {
		return result, err
	}

	if len(p.ExpectedSHA256) != 64 {
		return result, errors.New(
			"expected_sha256 must be a 64-character SHA-256 hex digest",
		)
	}

	if _, err := hex.DecodeString(
		p.ExpectedSHA256,
	); err != nil {
		return result, err
	}

	if p.Old == "" || p.Old == p.New {
		return result, errors.New(
			"patch must contain a nonempty old fragment and a different new fragment",
		)
	}

	if len(p.Old) > maxSourceBytes ||
		len(p.New) > maxSourceBytes {
		return result, errors.New(
			"patch fragment too large",
		)
	}

	original, err := os.ReadFile(target)
	if err != nil {
		return result, err
	}

	before := digest(original)

	if !strings.EqualFold(
		before,
		p.ExpectedSHA256,
	) {
		return result, fmt.Errorf(
			"stale patch: current SHA-256=%s, expected=%s",
			before,
			p.ExpectedSHA256,
		)
	}

	if bytes.Count(
		original,
		[]byte(p.Old),
	) != 1 {
		return result, errors.New(
			"old fragment must occur exactly once",
		)
	}

	updated := bytes.Replace(
		original,
		[]byte(p.Old),
		[]byte(p.New),
		1,
	)

	if len(updated) > maxSourceBytes {
		return result, errors.New(
			"patched file too large",
		)
	}

	// Detect changes already visible before writing.
	recent, err := os.ReadFile(target)

	if err != nil ||
		!bytes.Equal(recent, original) {
		return result, errors.New(
			"source changed while applying patch",
		)
	}

	temp, err := os.CreateTemp(
		filepath.Dir(target),
		".repair17-*.tmp",
	)
	if err != nil {
		return result, err
	}

	name := temp.Name()
	defer os.Remove(name)

	if err = temp.Chmod(
		st.Mode().Perm(),
	); err == nil {
		_, err = temp.Write(updated)
	}

	if err == nil {
		err = temp.Sync()
	}

	closeErr := temp.Close()

	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}
	if guard != nil {
		if err := guard(ctx); err != nil {
			return result, err
		}
	}
	if err := os.Rename(
		name, target,
	); err != nil {
		return result, err
	}

	return PatchResult{
		Path:         p.Path,
		BeforeSHA256: before,
		AfterSHA256:  digest(updated),
	}, nil
}
