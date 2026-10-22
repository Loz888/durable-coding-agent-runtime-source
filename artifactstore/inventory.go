package artifactstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// ReferenceSnapshot observes references in the caller-declared schemas. It must
// be read-only. Separate calls must observe separate committed snapshots.
// An inventory does not prove that the caller declared every publisher/schema.
type ReferenceSnapshot func(context.Context, []string) (map[string]struct{}, error)

// InventoryTotals counts directory entries; Bytes includes only regular files.
type InventoryTotals struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// InventoryReferences counts unique artifact keys, not referencing Run rows.
type InventoryReferences struct {
	Before           int `json:"before"`
	After            int `json:"after"`
	Appeared         int `json:"appeared_during_scan"`
	Disappeared      int `json:"disappeared_during_scan"`
	WithoutValidFile int `json:"observed_without_valid_file"`
}

type InventoryReport struct {
	ReadOnly              bool                       `json:"read_only"`
	SchemaCount           int                        `json:"declared_schema_count"`
	Scope                 string                     `json:"scope"`
	CompletenessProven    bool                       `json:"reference_completeness_proven"`
	DeletionAllowed       bool                       `json:"deletion_allowed"`
	AgeUsedForEligibility bool                       `json:"age_used_for_eligibility"`
	Warning               string                     `json:"warning"`
	Categories            map[string]InventoryTotals `json:"categories"`
	References            InventoryReferences        `json:"references"`
}

var inventorySchema = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// InventorySchemas validates an explicit nonempty list, without discovering or
// asserting that the list covers all consumers of an artifact root.
func InventorySchemas(schemas []string) error {
	if len(schemas) == 0 || len(schemas) > 32 {
		return errors.New("declare 1..32 distinct reference schemas")
	}
	seen := map[string]bool{}
	for _, schema := range schemas {
		if !inventorySchema.MatchString(schema) || seen[schema] {
			return errors.New("invalid or repeated reference schema")
		}
		seen[schema] = true
	}
	return nil
}

// ScanInventory only reads an existing private canonical root. It never creates
// directories, changes database references, or deletes blobs. Currently
// unreferenced is an observation, never deletion eligibility: Put may precede
// publication of its database reference, including after the second snapshot.
// File ages are deliberately not used, including when the wall clock changes.
func ScanInventory(ctx context.Context, root string, schemas []string, snapshot ReferenceSnapshot) (InventoryReport, error) {
	var out InventoryReport
	if err := InventorySchemas(schemas); err != nil {
		return out, err
	}
	if snapshot == nil {
		return out, errors.New("read-only reference reader required")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return out, errors.New("existing private canonical artifact root required")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return out, errors.New("existing private canonical artifact root required")
	}
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return out, errors.New("artifact root must be an existing private directory")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return out, errors.New("artifact root unavailable")
	}
	defer dir.Close()
	list, err := dir.Open(".")
	if err != nil {
		return out, errors.New("artifact root unavailable")
	}
	defer list.Close()
	opened, err := list.Stat()
	if err != nil || !os.SameFile(st, opened) {
		return out, errors.New("artifact root changed during observation")
	}
	before, err := inventoryReferences(ctx, schemas, snapshot)
	if err != nil {
		return out, err
	}
	out = InventoryReport{
		ReadOnly: true, SchemaCount: len(schemas), Scope: "caller_declared_schemas_only",
		Warning:    "Two read-only reference observations are not a consistent cross-store snapshot, a proof of complete schema coverage, or permission to delete. Uploads may publish references later. File ages are not deletion evidence.",
		Categories: map[string]InventoryTotals{},
	}
	for _, category := range []string{"referenced", "reference_appeared_during_scan", "reference_disappeared_during_scan", "currently_unreferenced", "invalid_content", "unmanaged_name", "symlink", "nonregular", "changed_during_scan", "unreadable"} {
		out.Categories[category] = InventoryTotals{}
	}
	type validFile struct {
		key   string
		bytes int64
	}
	var valid []validFile
	files := 0
	add := func(category string, size int64) {
		total := out.Categories[category]
		total.Files++
		total.Bytes += size
		out.Categories[category] = total
	}
	for {
		if ctx.Err() != nil {
			return InventoryReport{}, errors.New("inventory canceled")
		}
		entries, readErr := list.ReadDir(256)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return InventoryReport{}, errors.New("artifact directory observation failed")
		}
		for _, entry := range entries {
			files++
			if files > 100000 {
				return InventoryReport{}, errors.New("artifact inventory exceeds 100000 entries")
			}
			name := entry.Name()
			info, e := dir.Lstat(name)
			if e != nil {
				add("changed_during_scan", 0)
				continue
			}
			size := info.Size()
			if !info.Mode().IsRegular() {
				size = 0
			}
			if info.Mode()&os.ModeSymlink != 0 {
				add("symlink", 0)
				continue
			}
			if !info.Mode().IsRegular() {
				add("nonregular", 0)
				continue
			}
			if !hashPattern.MatchString(name) {
				add("unmanaged_name", size)
				continue
			}
			if info.Size() < 1 || info.Size() > MaxBytes {
				add("invalid_content", size)
				continue
			}
			f, e := dir.Open(name)
			if e != nil {
				add("unreadable", size)
				continue
			}
			current, e := f.Stat()
			if e != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
				f.Close()
				add("changed_during_scan", size)
				continue
			}
			h := sha256.New()
			n, e := io.Copy(h, io.LimitReader(f, MaxBytes+1))
			final, statErr := f.Stat()
			f.Close()
			afterInfo, pathErr := dir.Lstat(name)
			if statErr != nil || pathErr != nil || !os.SameFile(info, afterInfo) || !afterInfo.Mode().IsRegular() || final.Size() != info.Size() || !final.ModTime().Equal(info.ModTime()) {
				add("changed_during_scan", size)
				continue
			}
			if e != nil {
				add("unreadable", size)
				continue
			}
			if n != info.Size() || hex.EncodeToString(h.Sum(nil)) != name {
				add("invalid_content", size)
				continue
			}
			valid = append(valid, validFile{name, size})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	after, err := inventoryReferences(ctx, schemas, snapshot)
	if err != nil {
		return InventoryReport{}, err
	}
	finalRoot, err := os.Lstat(root)
	if err != nil || !os.SameFile(st, finalRoot) || !finalRoot.IsDir() || finalRoot.Mode().Perm()&0077 != 0 {
		return InventoryReport{}, errors.New("artifact root changed during observation")
	}
	out.References.Before = len(before)
	out.References.After = len(after)
	all := map[string]bool{}
	for key := range before {
		all[key] = true
		if _, ok := after[key]; !ok {
			out.References.Disappeared++
		}
	}
	for key := range after {
		all[key] = true
		if _, ok := before[key]; !ok {
			out.References.Appeared++
		}
	}
	for _, file := range valid {
		_, was := before[file.key]
		_, now := after[file.key]
		category := "currently_unreferenced"
		switch {
		case was && now:
			category = "referenced"
		case now:
			category = "reference_appeared_during_scan"
		case was:
			category = "reference_disappeared_during_scan"
		}
		add(category, file.bytes)
		delete(all, file.key)
	}
	out.References.WithoutValidFile = len(all)
	return out, nil
}

func inventoryReferences(ctx context.Context, schemas []string, snapshot ReferenceSnapshot) (map[string]struct{}, error) {
	refs, err := snapshot(ctx, append([]string(nil), schemas...))
	if err != nil {
		return nil, errors.New("read-only reference observation failed")
	}
	if len(refs) > 100000 {
		return nil, errors.New("reference observation exceeds 100000 keys")
	}
	copy := make(map[string]struct{}, len(refs))
	for key := range refs {
		if !hashPattern.MatchString(key) {
			return nil, errors.New("reference observation contains invalid artifact keys")
		}
		copy[key] = struct{}{}
	}
	return copy, nil
}
