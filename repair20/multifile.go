package repair20

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
)

func (a Agent) targets() ([]string, error) {
	files := append([]string(nil), a.Files...)
	if len(files) == 0 {
		files = []string{a.File}
	}
	if len(files) < 1 || len(files) > repair19.MaxFileChanges {
		return nil, errors.New("require 1..8 target files")
	}
	sort.Strings(files)
	for i, f := range files {
		if !filepath.IsLocal(f) || filepath.Clean(f) != f || filepath.Ext(f) != ".go" || strings.HasSuffix(f, "_test.go") || (i > 0 && files[i-1] == f) {
			return nil, errors.New("distinct canonical local non-test Go targets required")
		}
	}
	return files, nil
}
func (a Agent) currentRequest(ctx context.Context, w repair17.Workspace, round int, failure repair17.TestResult) (repair18.Request, error) {
	files, err := a.targets()
	if err != nil {
		return repair18.Request{}, err
	}
	var sources []repair18.SourceFile
	total := 0
	for _, file := range files {
		sha, e := repair17.Inspect(ctx, w.Path, file)
		if e != nil {
			return repair18.Request{}, e
		}
		raw, e := os.ReadFile(filepath.Join(w.Path, file))
		if e != nil {
			return repair18.Request{}, e
		}
		total += len(raw)
		if len(raw) > 16<<10 || total > 32<<10 {
			return repair18.Request{}, errors.New("bounded source context exceeded")
		}
		sources = append(sources, repair18.SourceFile{File: file, SHA256: sha, Source: string(raw)})
	}
	first := sources[0]
	request := repair18.Request{SourceCommit: w.Commit, SourceHash: first.SHA256, Round: round, File: first.File, Source: first.Source, TestOutput: clip(failure.Output, 8000), Feedback: "Fix CURRENT source. Use the latest test evidence."}
	if len(sources) > 1 {
		request.Files = sources
	}
	return request, nil
}
func proposalPatches(in repair18.Request, suggestion repair18.Suggestion) ([]repair17.Patch, error) {
	allowed := map[string]repair18.SourceFile{in.File: {File: in.File, SHA256: in.SourceHash, Source: in.Source}}
	for _, f := range in.Files {
		allowed[f.File] = f
	}
	changes := suggestion.Changes
	if len(changes) == 0 {
		changes = []repair18.Change{{File: in.File, Old: suggestion.Old, New: suggestion.New}}
	} else if suggestion.Old != "" || suggestion.New != "" {
		return nil, repair19.ErrConflict
	}
	if len(changes) > repair19.MaxFileChanges {
		return nil, repair19.ErrConflict
	}
	seen := map[string]bool{}
	patches := make([]repair17.Patch, 0, len(changes))
	for _, c := range changes {
		f, ok := allowed[c.File]
		if !ok || seen[c.File] || c.Old == "" || c.Old == c.New || len(c.Old) > 16<<10 || len(c.New) > 16<<10 {
			return nil, repair19.ErrConflict
		}
		seen[c.File] = true
		patches = append(patches, repair17.Patch{Path: c.File, ExpectedSHA256: f.SHA256, Old: c.Old, New: c.New})
	}
	return patches, nil
}
func (p PostgresLedger) PrepareBatch(ctx context.Context, r repair19.Ref, id string, patches []repair17.Patch) (repair19.Intent, error) {
	return p.Store.PrepareBatch(ctx, r, id, patches)
}

func (p PostgresLedger) Propose(ctx context.Context, r repair19.Ref, in repair18.Request, next repair18.Proposer, timeout time.Duration) (repair18.Suggestion, error) {
	return p.Store.Propose(ctx, r, in, next, timeout)
}
