package repair17

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Portable is a bounded tracked-file snapshot, not a Git clone or arbitrary archive.
// Baseline membership must exist on the receiving host; adds/deletes are unsupported.
type Portable struct {
	Version string         `json:"version"`
	RunID   string         `json:"run_id"`
	Commit  string         `json:"commit"`
	Files   []PortableFile `json:"files"`
}
type PortableFile struct {
	Path string `json:"path"`
	SHA  string `json:"sha256"`
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
}

func safePortablePath(path string) bool {
	if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") {
			return false
		}
	}
	return true
}
func portableFile(root, path string) (string, os.FileInfo, error) {
	if !safePortablePath(path) {
		return "", nil, errors.New("protected portable path")
	}
	target := filepath.Join(root, filepath.FromSlash(path))
	actual, e := filepath.EvalSymlinks(target)
	if e != nil || actual != target {
		return "", nil, errors.New("portable path missing or linked")
	}
	st, e := os.Lstat(target)
	if e != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return "", nil, errors.New("invalid portable file")
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return "", nil, errors.New("hard links refused")
	}
	return target, st, nil
}
func tracked(ctx context.Context, w Workspace) ([]string, error) {
	raw, e := git(ctx, w.Path, "ls-files", "-z")
	if e != nil {
		return nil, e
	}
	files := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	sort.Strings(files)
	if len(files) < 1 || len(files) > 256 {
		return nil, errors.New("portable file count bound exceeded")
	}
	// Include ignored/untracked files in the refusal; never silently omit private inputs.
	others, e := git(ctx, w.Path, "ls-files", "--others", "-z")
	if e != nil {
		return nil, e
	}
	for _, f := range strings.Split(strings.TrimSuffix(others, "\x00"), "\x00") {
		if f != "" && f != markerFile {
			return nil, errors.New("portable snapshot refuses untracked files")
		}
	}
	return files, nil
}
func MakePortable(ctx context.Context, path string) ([]byte, error) {
	w, e := OpenManaged(ctx, path)
	if e != nil {
		return nil, e
	}
	names, e := tracked(ctx, w)
	if e != nil {
		return nil, e
	}
	m := Portable{Version: "tracked-candidate-v1", RunID: w.RunID, Commit: w.Commit}
	total := 0
	for _, name := range names {
		target, st, e := portableFile(w.Path, name)
		if e != nil {
			return nil, e
		}
		data, e := os.ReadFile(target)
		if e != nil {
			return nil, e
		}
		total += len(data)
		if total > 4<<20 {
			return nil, errors.New("portable input byte bound exceeded")
		}
		m.Files = append(m.Files, PortableFile{name, digest(data), uint32(st.Mode().Perm()), data})
	}
	return json.Marshal(m)
}
func ParsePortable(raw []byte) (Portable, error) {
	var m Portable
	if len(raw) > 8<<20 || json.Unmarshal(raw, &m) != nil || m.Version != "tracked-candidate-v1" || !runIDPattern.MatchString(m.RunID) || len(m.Commit) != 40 || len(m.Files) < 1 || len(m.Files) > 256 {
		return m, errors.New("invalid portable manifest")
	}
	total := 0
	prior := ""
	for _, f := range m.Files {
		total += len(f.Data)
		if !safePortablePath(f.Path) || f.Path <= prior || f.SHA != digest(f.Data) || len(f.Data) > 1<<20 || total > 4<<20 || (f.Mode != 0600 && f.Mode != 0644 && f.Mode != 0755) {
			return m, errors.New("invalid portable member")
		}
		prior = f.Path
	}
	return m, nil
}

// RestorePortable accepts only identical trusted baseline membership. The caller
// must hold the authoritative Run lock and supply a fresh lease guard per rename.
func RestorePortable(ctx context.Context, path string, m Portable, guard func(context.Context) error) error {
	if guard == nil {
		return errors.New("portable restore requires lease guard")
	}
	w, e := OpenManaged(ctx, path)
	if e != nil {
		return e
	}
	if w.RunID != m.RunID || w.Commit != m.Commit {
		return errors.New("portable baseline mismatch")
	}
	raw, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if _, e = ParsePortable(raw); e != nil {
		return e
	}
	names, e := tracked(ctx, w)
	if e != nil {
		return e
	}
	if len(names) != len(m.Files) {
		return errors.New("portable membership mismatch")
	}
	for i, f := range m.Files {
		if names[i] != f.Path {
			return errors.New("portable membership mismatch")
		}
		if _, _, e = portableFile(path, f.Path); e != nil {
			return e
		}
	}
	stage, e := os.MkdirTemp(filepath.Dir(w.Path), ".portable-stage-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	for _, f := range m.Files {
		target, _, e := portableFile(path, f.Path)
		if e != nil {
			return e
		}
		temp, e := os.CreateTemp(stage, "member-")
		if e != nil {
			return e
		}
		if e = temp.Chmod(os.FileMode(f.Mode)); e == nil {
			_, e = temp.Write(f.Data)
		}
		if e == nil {
			e = temp.Sync()
		}
		ce := temp.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = guard(ctx)
		}
		if e == nil {
			e = os.Rename(temp.Name(), target)
		}
		os.Remove(temp.Name())
		if e != nil {
			return e
		}
	}
	now, e := MakePortable(ctx, path)
	if e != nil {
		return e
	}
	if string(now) != string(raw) {
		return errors.New("portable restore verification mismatch")
	}
	return nil
}
