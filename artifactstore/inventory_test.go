package artifactstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inventoryRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func inventoryBlob(t *testing.T, root string, b []byte) string {
	t.Helper()
	key := Digest(b)
	if err := os.WriteFile(filepath.Join(root, key), b, 0600); err != nil {
		t.Fatal(err)
	}
	return key
}
func fixedReferences(keys ...string) ReferenceSnapshot {
	return func(context.Context, []string) (map[string]struct{}, error) {
		refs := map[string]struct{}{}
		for _, key := range keys {
			refs[key] = struct{}{}
		}
		return refs, nil
	}
}

func TestInventoryClassifiesWithoutChangingFilesOrLeakingIdentities(t *testing.T) {
	root := inventoryRoot(t)
	referenced := inventoryBlob(t, root, []byte("private referenced content"))
	inventoryBlob(t, root, []byte("private unreferenced content"))
	corrupt := Digest([]byte("expected bytes"))
	if err := os.WriteFile(filepath.Join(root, corrupt), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "private-token-do-not-print")
	if err := os.WriteFile(secret, []byte("private token payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, Digest([]byte("symbolic")))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, Digest([]byte("directory"))), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".upload-private"), []byte("upload"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ScanInventory(context.Background(), root, []string{"day19"}, fixedReferences(referenced, corrupt))
	if err != nil {
		t.Fatal(err)
	}
	for category, count := range map[string]int64{"referenced": 1, "currently_unreferenced": 1, "invalid_content": 1, "symlink": 1, "nonregular": 1, "unmanaged_name": 1} {
		if report.Categories[category].Files != count {
			t.Fatalf("category %s: %+v", category, report.Categories)
		}
	}
	if report.References.WithoutValidFile != 1 || report.DeletionAllowed || report.CompletenessProven || report.AgeUsedForEligibility || !report.ReadOnly {
		t.Fatalf("unsafe report: %+v", report)
	}
	if report.Categories["referenced"].Bytes != int64(len("private referenced content")) {
		t.Fatal("size missing")
	}
	output, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, secret, referenced, corrupt, "private referenced content", "private unreferenced content", "private token payload", ".upload-private"} {
		if strings.Contains(string(output), private) {
			t.Fatal("inventory leaked a private identity or payload")
		}
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("inventory changed directory membership")
	}
	payload, err := os.ReadFile(filepath.Join(root, corrupt))
	if err != nil || string(payload) != "corrupt" {
		t.Fatal("inventory modified invalid content")
	}
	link, err := os.Readlink(filepath.Join(root, Digest([]byte("symbolic"))))
	if err != nil || link != secret {
		t.Fatal("inventory modified symlink")
	}
}

func TestInventoryObservesPublicationAndReferenceRemovalWithoutDeletionEligibility(t *testing.T) {
	root := inventoryRoot(t)
	added := inventoryBlob(t, root, []byte("uploaded but reference not yet committed"))
	removed := inventoryBlob(t, root, []byte("reference removed during scan"))
	calls := 0
	reader := func(ctx context.Context, schemas []string) (map[string]struct{}, error) {
		calls++
		if len(schemas) != 1 || schemas[0] != "day19" {
			t.Fatal("unexpected schemas")
		}
		schemas[0] = "mutated-copy"
		if calls == 1 {
			return map[string]struct{}{removed: {}}, nil
		}
		return map[string]struct{}{added: {}}, nil
	}
	schemas := []string{"day19"}
	report, err := ScanInventory(context.Background(), root, schemas, reader)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || schemas[0] != "day19" {
		t.Fatal("reference reads not isolated")
	}
	if report.Categories["reference_appeared_during_scan"].Files != 1 || report.Categories["reference_disappeared_during_scan"].Files != 1 || report.References.Appeared != 1 || report.References.Disappeared != 1 {
		t.Fatalf("publication race hidden: %+v", report)
	}
	if report.DeletionAllowed || report.CompletenessProven || !strings.Contains(report.Warning, "Uploads may publish references later") {
		t.Fatal("reference observation wrongly treated as deletion permission")
	}
}

func TestInventoryPublicationAfterDirectoryScanIsMissingObservationNotSafeDeletion(t *testing.T) {
	root := inventoryRoot(t)
	calls := 0
	reader := func(context.Context, []string) (map[string]struct{}, error) {
		calls++
		refs := map[string]struct{}{}
		if calls == 2 {
			refs[inventoryBlob(t, root, []byte("uploaded after directory scan"))] = struct{}{}
		}
		return refs, nil
	}
	report, err := ScanInventory(context.Background(), root, []string{"day19"}, reader)
	if err != nil {
		t.Fatal(err)
	}
	if report.References.WithoutValidFile != 1 || report.References.Appeared != 1 || report.DeletionAllowed {
		t.Fatalf("late publication wrongly inferred complete: %+v", report)
	}
}

func TestInventoryRejectsUnsafeInputsWithoutCreatingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := ScanInventory(context.Background(), missing, []string{"day19"}, fixedReferences()); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only inventory created root")
	}
	root := inventoryRoot(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{alias, root + "/.", "relative"} {
		if _, err := ScanInventory(context.Background(), bad, []string{"day19"}, fixedReferences()); err == nil {
			t.Fatal("noncanonical root accepted")
		}
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanInventory(context.Background(), root, []string{"day19"}, fixedReferences()); err == nil {
		t.Fatal("shared root accepted")
	}
	for _, schemas := range [][]string{nil, {"day19", "day19"}, {"day19; DELETE FROM audit"}, {""}} {
		if err := InventorySchemas(schemas); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
}

func TestInventoryReaderFailureAndInvalidKeysAreRedacted(t *testing.T) {
	root := inventoryRoot(t)
	secret := "postgres://private-password@private-host/private-database"
	for _, reader := range []ReferenceSnapshot{
		func(context.Context, []string) (map[string]struct{}, error) { return nil, errors.New(secret) },
		fixedReferences(secret),
	} {
		report, err := ScanInventory(context.Background(), root, []string{"day19"}, reader)
		if err == nil || strings.Contains(err.Error(), secret) || report.ReadOnly {
			t.Fatal("private or incomplete failure report")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanInventory(ctx, root, []string{"day19"}, fixedReferences()); err == nil {
		t.Fatal("canceled inventory succeeded")
	}
}
