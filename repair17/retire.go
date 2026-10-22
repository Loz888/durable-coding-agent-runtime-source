package repair17

import (
	"context"
	"errors"
	"path/filepath"
)

// RemoveManaged never removes the source clone; callers serialize lifecycle changes.
func RemoveManaged(ctx context.Context, path, identity, base string, guard func(context.Context) error) error {
	w, e := OpenManaged(ctx, path)
	if e != nil {
		return e
	}
	canonical, e := filepath.EvalSymlinks(base)
	if e != nil || canonical != base || w.Path == base || !inside(base, w.Path) || w.Identity == "" || w.Identity != identity || guard == nil {
		return errors.New("managed retirement identity/base mismatch")
	}
	if e = guard(ctx); e != nil {
		return e
	}
	_, e = git(ctx, w.Source, "worktree", "remove", "--force", w.Path)
	return e
}
