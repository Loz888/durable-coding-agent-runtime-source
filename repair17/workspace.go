package repair17

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const markerFile = ".repair17.json"

var runIDPattern = regexp.MustCompile(
	`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`,
)

type Workspace struct {
	RunID    string `json:"run_id"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Commit   string `json:"commit"`
	GitDir   string `json:"git_dir,omitempty"`
	Identity string `json:"identity,omitempty"`
}

func git(
	ctx context.Context,
	dir string,
	args ...string,
) (string, error) {
	argv := append(
		[]string{"-C", dir},
		args...,
	)

	cmd := exec.CommandContext(
		ctx, "git", argv...,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"git %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(out)),
		)
	}

	return strings.TrimSpace(string(out)), nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)

	return errA == nil &&
		errB == nil &&
		filepath.Clean(aa) == filepath.Clean(bb)
}

func inside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)

	return err == nil &&
		(rel == "." ||
			(rel != ".." &&
				!strings.HasPrefix(
					rel,
					".."+string(filepath.Separator),
				)))
}

// Requires a clean independent Git repository.
func Prepare(
	ctx context.Context,
	source string,
	base string,
	runID string,
) (Workspace, error) {
	var w Workspace

	if !runIDPattern.MatchString(runID) {
		return w, errors.New("invalid run ID")
	}

	src, err := filepath.Abs(source)
	if err != nil {
		return w, err
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return w, err
	}

	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return w, err
	}

	if inside(src, baseAbs) {
		return w, errors.New(
			"worktree base must be outside source repository",
		)
	}

	root, err := git(
		ctx, src,
		"rev-parse", "--show-toplevel",
	)
	if err != nil {
		return w, err
	}

	if !samePath(root, src) {
		return w, errors.New(
			"source must be its own Git repository root",
		)
	}

	status, err := git(
		ctx, src,
		"status", "--porcelain",
		"--untracked-files=all",
	)
	if err != nil {
		return w, err
	}

	if status != "" {
		return w, errors.New(
			"source repository must be clean before preparing a worktree",
		)
	}

	commit, err := git(
		ctx, src,
		"rev-parse", "HEAD",
	)
	if err != nil {
		return w, err
	}

	if err := os.MkdirAll(baseAbs, 0755); err != nil {
		return w, err
	}

	baseAbs, err = filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return w, err
	}

	if inside(src, baseAbs) {
		return w, errors.New(
			"worktree base resolves inside source",
		)
	}

	path := filepath.Join(baseAbs, runID)

	if _, err := os.Lstat(path); err == nil {
		return w, errors.New(
			"worktree path already exists",
		)
	} else if !os.IsNotExist(err) {
		return w, err
	}

	_, err = git(
		ctx, src,
		"worktree", "add",
		"--detach", path, commit,
	)
	if err != nil {
		return w, err
	}

	w = Workspace{
		RunID:  runID,
		Source: src,
		Path:   path,
		Commit: commit,
	}

	gitDir, err := git(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Workspace{}, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return Workspace{}, err
	}
	w.GitDir = gitDir
	w.Identity = hex.EncodeToString(token[:])
	identityFile, err := os.OpenFile(filepath.Join(gitDir, "repair17-identity"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Workspace{}, err
	}
	_, writeErr := identityFile.WriteString(w.Identity)
	closeErr := identityFile.Close()
	if writeErr != nil {
		return Workspace{}, writeErr
	}
	if closeErr != nil {
		return Workspace{}, closeErr
	}
	b, err := json.MarshalIndent(
		w, "", "  ",
	)

	if err == nil {
		err = os.WriteFile(
			filepath.Join(path, markerFile),
			b,
			0600,
		)
	}

	if err != nil {
		_, _ = git(
			context.Background(),
			src,
			"worktree", "remove",
			"--force", path,
		)

		return Workspace{}, err
	}

	return w, nil
}

// Only opens linked worktrees created by Prepare.
func OpenManaged(
	ctx context.Context,
	path string,
) (Workspace, error) {
	var w Workspace

	abs, err := filepath.Abs(path)
	if err != nil {
		return w, err
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || resolved != abs {
		return w, errors.New("managed worktree path is missing or contains symlinks")
	}
	marker, err := os.Lstat(filepath.Join(abs, markerFile))
	if err != nil || !marker.Mode().IsRegular() || marker.Size() > 16384 {
		return w, errors.New("invalid managed-worktree marker")
	}
	b, err := os.ReadFile(
		filepath.Join(abs, markerFile),
	)
	if err != nil {
		return w, fmt.Errorf(
			"missing managed-worktree marker: %w",
			err,
		)
	}

	if err := json.Unmarshal(b, &w); err != nil {
		return Workspace{}, err
	}

	if !samePath(abs, w.Path) ||
		!runIDPattern.MatchString(w.RunID) {
		return Workspace{}, errors.New(
			"worktree metadata mismatch",
		)
	}

	root, err := git(
		ctx, abs,
		"rev-parse", "--show-toplevel",
	)

	if err != nil || !samePath(root, abs) {
		return Workspace{}, errors.New(
			"not the expected Git worktree root",
		)
	}

	commit, err := git(
		ctx, abs,
		"rev-parse", "HEAD",
	)

	if err != nil || commit != w.Commit {
		return Workspace{}, errors.New(
			"worktree baseline commit changed",
		)
	}

	linked, err := os.Lstat(
		filepath.Join(abs, ".git"),
	)

	if err != nil || !linked.Mode().IsRegular() {
		return Workspace{}, errors.New(
			"not a linked worktree",
		)
	}

	actualDir, err := git(ctx, abs, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Workspace{}, err
	}
	common, err := git(ctx, abs, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Workspace{}, err
	}
	sourceCommon, err := git(ctx, w.Source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !samePath(common, sourceCommon) {
		return Workspace{}, errors.New("worktree source repository changed")
	}
	backlink, err := os.ReadFile(filepath.Join(actualDir, "gitdir"))
	if err != nil || !samePath(strings.TrimSpace(string(backlink)), filepath.Join(abs, ".git")) {
		return Workspace{}, errors.New("worktree registration mismatch")
	}
	if w.GitDir != "" && !samePath(actualDir, w.GitDir) {
		return Workspace{}, errors.New("worktree Git identity changed")
	}
	if w.Identity != "" {
		identity, err := os.ReadFile(filepath.Join(actualDir, "repair17-identity"))
		if err != nil || string(identity) != w.Identity {
			return Workspace{}, errors.New("managed worktree identity changed")
		}
	}
	return w, nil
}

func Diff(
	ctx context.Context,
	worktree string,
) (string, error) {
	w, err := OpenManaged(ctx, worktree)
	if err != nil {
		return "", err
	}

	return git(
		ctx, w.Path,
		"diff", "--no-ext-diff",
		"--", ".",
	)
}

// MarkDurable protects controlled Day17/18 entry points once a workspace is
// bound to durable repair. A failed database commit may conservatively leave
// this marker; it must be audited, never silently removed to permit a write.
func MarkDurable(ctx context.Context, path string) error {
	w, err := OpenManaged(ctx, path)
	if err != nil {
		return err
	}
	if w.Identity == "" || w.GitDir == "" {
		return errors.New("durable binding requires managed identity")
	}
	marker := filepath.Join(w.GitDir, "repair19-bound")
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(marker)
		if readErr != nil {
			return readErr
		}
		if string(existing) != w.Identity {
			return errors.New("durable binding marker mismatch")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(w.Identity)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
