package pilotapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/durable"
	"runtime-app/internal/testdb"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Server, http.Handler, string, string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated test database required; no SKIP")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var db string
	if err = pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); err != nil || db != "agent_runtime_day14_test" {
		t.Fatal("not dedicated test db", err)
	}
	testdb.LockQueue(t, pool)
	data, err := os.ReadFile("../migrations/007_pilot_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(data)); err != nil {
		t.Fatal(err)
	}
	label := strings.ReplaceAll(t.Name(), "/", "_") + time.Now().Format("150405.000000")
	a := "synthetic-pilot-token-a-" + label
	b := "synthetic-pilot-token-b-" + label
	_, err = pool.Exec(ctx, `INSERT INTO pilot.teams(team,max_pending,per_minute) VALUES($1,2,100),($2,2,100)`, label+"a", label+"b")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ token, team string }{{a, label + "a"}, {b, label + "b"}} {
		_, err = pool.Exec(ctx, `INSERT INTO pilot.tokens(token_hash,principal,team,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '1 hour')`, HashToken(v.token), "user", v.team)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM day12.agent_runs WHERE run_id IN(SELECT run_id FROM pilot.runs WHERE team=$1 OR team=$2)`, label+"a", label+"b")
		_, _ = pool.Exec(ctx, `DELETE FROM pilot.tokens WHERE team=$1 OR team=$2`, label+"a", label+"b")
		_, _ = pool.Exec(ctx, `DELETE FROM pilot.teams WHERE team=$1 OR team=$2`, label+"a", label+"b")
	})
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	raw, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	// Separate operator fixture pool from the actual API role; credentials are never in HTTP.
	if _, err = pool.Exec(ctx, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='runtime_pilot_api_test') THEN CREATE ROLE runtime_pilot_api_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS; END IF; END $$; GRANT USAGE ON SCHEMA day12,day19,pilot TO runtime_pilot_api_test; GRANT SELECT,INSERT ON day12.agent_runs,day12.checkpoints,day12.run_events TO runtime_pilot_api_test; GRANT UPDATE(status,failure_kind,failure_reason,finished_at,lease_owner,lease_until) ON day12.agent_runs TO runtime_pilot_api_test; GRANT SELECT ON pilot.tokens,pilot.teams,day19.patch_ops TO runtime_pilot_api_test; GRANT SELECT,INSERT,UPDATE ON pilot.runs TO runtime_pilot_api_test; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA day12 TO runtime_pilot_api_test;`); err != nil {
		t.Fatal(err)
	}
	// Use an actual restricted login for TCP/SCRAM CI as well as socket tests.
	// This password belongs only to the isolated test fixture.
	password := rand.Text()
	var passwordSQL string
	if err = pool.QueryRow(ctx, `SELECT format('ALTER ROLE runtime_pilot_api_test PASSWORD %L', $1::text)`, password).Scan(&passwordSQL); err != nil {
		t.Fatal("format fixture password", err)
	}
	if _, err = pool.Exec(ctx, passwordSQL); err != nil {
		t.Fatal("configure fixture login", err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.User = "runtime_pilot_api_test"
	config.ConnConfig.Password = password
	apiPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(apiPool.Close)
	s := &Server{Pool: apiPool, MaxQueued: 100, Repositories: map[string]Repository{"repo": {Path: filepath.Clean(repo), Commit: strings.TrimSpace(string(raw)), Teams: []string{label + "a", label + "b"}}}}
	h, err := s.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, h, a, b, pool
}
func call(h http.Handler, method, path, token string, data any) *httptest.ResponseRecorder {
	var raw []byte
	if data != nil {
		raw, _ = json.Marshal(data)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestIdentityOwnershipIdempotencyAndCancellation(t *testing.T) {
	s, h, a, b, admin := fixture(t)
	in := submission{Repository: "repo", Commit: s.Repositories["repo"].Commit, Goal: "fix controlled fixture", IdempotencyKey: "same-key"}
	if w := call(h, "POST", "/v1/runs", "", in); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call(h, "POST", "/v1/runs", a, in)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		RunID    string `json:"run_id"`
		Replayed bool   `json:"replayed"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.RunID == "" {
		t.Fatal(w.Body.String())
	}
	id := out.RunID
	w = call(h, "POST", "/v1/runs", a, in)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	in.Goal = "different"
	if w = call(h, "POST", "/v1/runs", a, in); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	in.Goal = "fix controlled fixture"
	for _, path := range []string{"", "/events", "/patches", "/cancel"} {
		method := "GET"
		if path == "/cancel" {
			method = "POST"
		}
		w = call(h, method, "/v1/runs/"+id+path, b, nil)
		if w.Code != 404 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w = call(h, "POST", "/v1/runs", b, in)
	if w.Code != 202 {
		t.Fatal("same idempotency key must be independently scoped", w.Code, w.Body.String())
	}
	if w = call(h, "GET", "/v1/runs/"+id, a, nil); w.Code != 200 || strings.Contains(w.Body.String(), s.Repositories["repo"].Path) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Real ClaimBatch, then revoke while an owner is active; stale completion must fail.
	legacyJobs, e := (durable.Queue{Pool: admin}).ClaimBatch(context.Background(), "legacy-cannot-run-pilot", 2, time.Second)
	if e != nil || len(legacyJobs) != 0 {
		t.Fatal("legacy host worker can claim pilot jobs", legacyJobs, e)
	}
	jobs, err := (durable.Queue{Pool: admin, Pilot: true}).ClaimBatch(context.Background(), "pilot-cancel-test", 2, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var lease durable.Lease
	for _, j := range jobs {
		if j.RunID == id {
			lease = j.Lease
		}
	}
	if lease.RunID == "" {
		t.Fatal("real queue did not claim owned run")
	}
	for i := 0; i < 2; i++ {
		if w = call(h, "POST", "/v1/runs/"+id+"/cancel", a, nil); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	snap, err := (durable.PostgresStore{Pool: admin}).Load(context.Background(), id)
	if err != nil || snap.Status != durable.Failed {
		t.Fatal(snap, err)
	}
	snap.Status = durable.Succeeded
	snap.Revision++
	snap.Final = "stale success"
	if err = (durable.FencedStore{Pool: admin, Lease: lease}).Save(context.Background(), snap, snap.Revision-1); err == nil {
		t.Fatal("cancelled stale owner committed success")
	}
	_, err = admin.Exec(context.Background(), `UPDATE pilot.tokens SET revoked_at=clock_timestamp() WHERE token_hash=$1`, HashToken(a))
	if err != nil {
		t.Fatal(err)
	}
	if w = call(h, "GET", "/v1/runs/"+id, a, nil); w.Code != 401 {
		t.Fatal("revocation not observed", w.Code)
	}
}
func TestRequestAndQueueLimits(t *testing.T) {
	s, h, a, _, _ := fixture(t)
	in := submission{Repository: "repo", Commit: s.Repositories["repo"].Commit, Goal: "fix", IdempotencyKey: "one"}
	if w := call(h, "POST", "/v1/runs", a, map[string]any{"team": "forged", "repository": "repo"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	bad := in
	bad.Repository = "/etc"
	if w := call(h, "POST", "/v1/runs", a, bad); w.Code != 404 {
		t.Fatal(w.Code)
	}
	bad = in
	bad.Goal = strings.Repeat("x", 9000)
	if w := call(h, "POST", "/v1/runs", a, bad); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, k := range []string{"one", "two", "three"} {
		in.IdempotencyKey = k
		w := call(h, "POST", "/v1/runs", a, in)
		want := 202
		if k == "three" {
			want = 429
		}
		if w.Code != want {
			t.Fatal(k, w.Code, w.Body.String())
		}
	}
	in.IdempotencyKey = "one"
	if w := call(h, "POST", "/v1/runs", a, in); w.Code != 202 {
		t.Fatal("replay rejected at capacity", w.Code)
	}
}
