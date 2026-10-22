//go:build linux

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ReapResult struct {
	Inspected        int `json:"inspected"`
	Expired          int `json:"expired"`
	Removed          int `json:"removed"`
	SnapshotsRemoved int `json:"snapshots_removed"`
	Retained         int `json:"retained"`
}

func (s GoSandbox) cleanupGrace() time.Duration {
	if s.CleanupGrace == 0 {
		return time.Minute
	}
	return s.CleanupGrace
}

// ReapExpired is a separate supervisor operation. It never guesses ownership
// from a global name prefix and never removes an unexpired container.
func (s GoSandbox) ReapExpired(ctx context.Context) (ReapResult, error) {
	var out ReapResult
	if e := s.Validate(); e != nil {
		return out, e
	}
	root, e := filepath.EvalSymlinks(s.Root)
	if e != nil || root != s.Root {
		return out, errors.New("canonical owned root required")
	}
	st, e := os.Stat(root)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return out, errors.New("private owned root required")
	}
	list := exec.CommandContext(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=io.agent-runtime.sandbox=owned-v1", "--filter", "label=io.agent-runtime.root="+HashRoot(root))
	raw, e := list.Output()
	if e != nil {
		return out, errors.New("sandbox supervisor unavailable")
	}
	ids := strings.Fields(string(raw))
	if len(ids) > 1000 {
		return out, errors.New("reaper batch bound exceeded")
	}
	idPattern := regexp.MustCompile(`^[a-f0-9]{64}$`)
	namePattern := regexp.MustCompile(`^candidate-[0-9]+$`)
	for _, id := range ids {
		if !idPattern.MatchString(id) {
			return out, errors.New("invalid container identity")
		}
		command := exec.CommandContext(ctx, "docker", "inspect", "--format", `{"labels":{{json .Config.Labels}},"mounts":{{json .Mounts}}}`, id)
		raw, e = command.Output()
		if e != nil {
			return out, errors.New("owned container inspection failed")
		}
		var info struct {
			Labels map[string]string                            `json:"labels"`
			Mounts []struct{ Type, Source, Destination string } `json:"mounts"`
		}
		if json.Unmarshal(raw, &info) != nil {
			return out, errors.New("invalid supervisor metadata")
		}
		out.Inspected++
		expires, e := strconv.ParseInt(info.Labels["io.agent-runtime.expires"], 10, 64)
		if e != nil || info.Labels["io.agent-runtime.sandbox"] != "owned-v1" || info.Labels["io.agent-runtime.root"] != HashRoot(root) || expires > time.Now().Unix() {
			out.Retained++
			continue
		}
		out.Expired++
		if e = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); e != nil {
			return out, errors.New("owned expired container removal failed")
		}
		out.Removed++
		// The immutable label AND actual bind mount must agree. Legacy/missing
		// metadata is retained for audit; no path outside this private root is used.
		name := info.Labels["io.agent-runtime.snapshot"]
		if !namePattern.MatchString(name) {
			continue
		}
		path := filepath.Join(root, name)
		mounted := false
		for _, m := range info.Mounts {
			if m.Type == "bind" && m.Destination == "/src" && m.Source == path {
				mounted = true
			}
		}
		if !mounted {
			continue
		}
		entry, e := os.Lstat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return out, errors.New("snapshot identity unavailable")
		}
		canonical, e := filepath.EvalSymlinks(path)
		if e != nil || canonical != path {
			return out, errors.New("snapshot path changed")
		}
		if e = os.RemoveAll(path); e != nil {
			return out, errors.New("owned orphan snapshot cleanup failed")
		}
		out.SnapshotsRemoved++
	}
	return out, nil
}
