// Package pilotapi adds ownership to the existing durable.Queue, not another scheduler.
package pilotapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime-app/observability"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"runtime-app/durable"
)

type Identity struct{ Principal, Team string }

// Repository registrations are operator-controlled; no host path is accepted by HTTP.
type Repository struct {
	Path, Commit string
	Teams        []string
}
type Server struct {
	AllowPaid    bool
	Pool         *pgxpool.Pool
	Repositories map[string]Repository
	MaxQueued    int
}
type submission struct {
	Provider       string `json:"provider,omitempty"`
	Repository     string `json:"repository"`
	Commit         string `json:"commit"`
	Goal           string `json:"goal"`
	IdempotencyKey string `json:"idempotency_key"`
}

var key = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var commit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var errMissing = errors.New("not found")
var errConflict = errors.New("idempotency conflict")
var errCapacity = errors.New("capacity")

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s *Server) Handler(ctx context.Context) (http.Handler, error) {
	if s.Pool == nil || s.MaxQueued < 1 || len(s.Repositories) == 0 {
		return nil, errors.New("pool, repositories and bounded queue required")
	}
	var unsafeRole bool
	if err := s.Pool.QueryRow(ctx, `SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname IN ('day12','pilot') AND nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)) FROM pg_roles WHERE rolname=current_user`).Scan(&unsafeRole); err != nil {
		return nil, err
	}
	if unsafeRole {
		return nil, errors.New("pilot API requires a restricted non-owner runtime role")
	}
	var configured bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pilot.tokens WHERE revoked_at IS NULL AND expires_at>clock_timestamp())`).Scan(&configured); err != nil {
		return nil, err
	}
	if !configured {
		return nil, errors.New("no active hashed tokens; refusing startup")
	}
	for id, repo := range s.Repositories {
		if !key.MatchString(id) || !filepath.IsAbs(repo.Path) || !commit.MatchString(repo.Commit) || len(repo.Teams) == 0 {
			return nil, errors.New("invalid repository registration")
		}
		cmd := exec.CommandContext(ctx, "git", "-C", repo.Path, "rev-parse", "HEAD")
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != repo.Commit {
			return nil, errors.New("registered repository commit mismatch")
		}
	}
	return http.HandlerFunc(s.serve), nil
}
func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.Path == "/health" && r.Method == "GET" {
		reply(w, 200, map[string]string{"status": "alive"})
		return
	}
	if r.URL.Path == "/ready" && r.Method == "GET" {
		if s.Pool.Ping(ctx) != nil {
			reply(w, 503, map[string]string{"status": "unavailable"})
		} else {
			reply(w, 200, map[string]string{"status": "ready"})
		}
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(token) < 24 || len(token) > 512 {
		reply(w, 401, map[string]string{"error": "invalid credentials"})
		return
	}
	var who Identity
	err := s.Pool.QueryRow(ctx, `SELECT principal,team FROM pilot.tokens WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp()`, HashToken(token)).Scan(&who.Principal, &who.Team)
	if errors.Is(err, pgx.ErrNoRows) {
		reply(w, 401, map[string]string{"error": "invalid credentials"})
		return
	}
	if err != nil {
		reply(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	if r.URL.Path == "/v1/runs" && r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var in submission
		if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !key.MatchString(in.IdempotencyKey) || len(strings.TrimSpace(in.Goal)) == 0 || len(in.Goal) > 4000 {
			reply(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		if in.Provider != "" && in.Provider != "coding:mock" && !(s.AllowPaid && in.Provider == "coding:deepseek") {
			reply(w, 400, map[string]string{"error": "unsupported pilot provider; paid capability awaits sandbox acceptance"})
			return
		}
		repo, found := s.Repositories[in.Repository]
		allowed := false
		for _, team := range repo.Teams {
			allowed = allowed || team == who.Team
		}
		if !found || !allowed {
			reply(w, 404, map[string]string{"error": "repository not found"})
			return
		}
		if in.Commit != repo.Commit {
			reply(w, 409, map[string]string{"error": "registered commit required"})
			return
		}
		id, replay, err := s.submit(ctx, who, in, repo)
		switch {
		case errors.Is(err, errConflict):
			reply(w, 409, map[string]string{"error": "idempotency conflict"})
		case errors.Is(err, errCapacity):
			w.Header().Set("Retry-After", "60")
			reply(w, 429, map[string]string{"error": "capacity exceeded"})
		case err != nil:
			reply(w, 503, map[string]string{"error": "unavailable"})
		default:
			reply(w, 202, map[string]any{"run_id": id, "replayed": replay})
		}
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "v1" || parts[1] != "runs" || !key.MatchString(parts[2]) {
		reply(w, 404, map[string]string{"error": "not found"})
		return
	}
	id := parts[2]
	var owned bool
	err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pilot.runs WHERE run_id=$1 AND team=$2 AND principal=$3)`, id, who.Team, who.Principal).Scan(&owned)
	if err != nil {
		reply(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	if !owned {
		reply(w, 404, map[string]string{"error": "not found"})
		return
	}
	if len(parts) == 3 && r.Method == "GET" {
		snap, err := (durable.PostgresStore{Pool: s.Pool}).Load(ctx, id)
		if err != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		var cancelled bool
		err = s.Pool.QueryRow(ctx, `SELECT cancel_requested FROM pilot.runs WHERE run_id=$1`, id).Scan(&cancelled)
		if err != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		// Raw history, paths, goal and error bodies deliberately excluded.
		reply(w, 200, map[string]any{"run_id": id, "status": snap.Status, "execution_status": snap.Status, "outcome_verdict": snap.OutcomeVerdict, "revision": snap.Revision, "diagnosis_model_turns": snap.Turns, "tool_calls": snap.Calls, "cancel_requested": cancelled})
		return
	}
	if len(parts) == 4 && parts[3] == "cancel" && r.Method == "POST" {
		err = s.cancel(ctx, id, who)
		if err != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
		} else {
			var requested bool
			if e := s.Pool.QueryRow(ctx, `SELECT cancel_requested FROM pilot.runs WHERE run_id=$1`, id).Scan(&requested); e != nil {
				reply(w, 503, map[string]string{"error": "unavailable"})
				return
			}
			reply(w, 200, map[string]any{"run_id": id, "cancel_requested": requested, "cleanup": "owner observes lease revocation; asynchronous"})
		}
		return
	}
	if len(parts) == 4 && parts[3] == "events" && r.Method == "GET" {
		after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if r.URL.Query().Get("after") == "" {
			after = 0
			err = nil
		}
		if err != nil || after < 0 {
			reply(w, 400, map[string]string{"error": "invalid cursor"})
			return
		}
		rows, err := s.Pool.Query(ctx, `SELECT event_id,event_type,status,revision,lease_epoch FROM day12.run_events WHERE run_id=$1 AND event_id>$2 ORDER BY event_id LIMIT 100`, id, after)
		if err != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		defer rows.Close()
		events := []map[string]any{}
		for rows.Next() {
			var eid, rev, epoch int64
			var kind, status string
			if rows.Scan(&eid, &kind, &status, &rev, &epoch) != nil {
				reply(w, 503, map[string]string{"error": "unavailable"})
				return
			}
			events = append(events, map[string]any{"event_id": eid, "type": kind, "status": status, "revision": rev, "epoch": epoch})
		}
		if rows.Err() != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		reply(w, 200, events)
		return
	}
	if len(parts) == 4 && parts[3] == "patches" && r.Method == "GET" {
		rows, err := s.Pool.Query(ctx, `SELECT patch_id,file_path,before_sha,after_sha,state,left(diff,65536),test_exit_code FROM day19.patch_ops WHERE run_id=$1 ORDER BY created_at LIMIT 10`, id)
		if err != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var pid, file, before, after, state, diff string
			var exit *int
			if rows.Scan(&pid, &file, &before, &after, &state, &diff, &exit) != nil {
				reply(w, 503, map[string]string{"error": "unavailable"})
				return
			}
			out = append(out, map[string]any{"patch_id": pid, "file": file, "before_sha": before, "after_sha": after, "state": state, "diff": diff, "exit_code": exit})
		}
		if rows.Err() != nil {
			reply(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		reply(w, 200, out)
		return
	}
	reply(w, 404, map[string]string{"error": "not found"})
}
func (s *Server) submit(ctx context.Context, who Identity, in submission, repo Repository) (resultID string, replayed bool, err error) {
	ctx, span := observability.StartOperation(ctx, "api.submit")
	defer func() {
		if resultID != "" {
			span.SetAttributes(attribute.String("run.id", resultID))
		}
		observability.FinishOperation(span, err)
	}()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	// Global capacity first, then team/key: one lock order in every API process.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2071019)`); err != nil {
		return "", false, err
	}
	data, _ := json.Marshal(in)
	digest := HashToken(string(data))
	var id, old string
	err = tx.QueryRow(ctx, `SELECT run_id,request_hash FROM pilot.runs WHERE team=$1 AND principal=$2 AND idempotency_key=$3`, who.Team, who.Principal, in.IdempotencyKey).Scan(&id, &old)
	if err == nil {
		if old != digest {
			return "", false, errConflict
		}
		return id, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var pending, rate, maxPending, perMinute, global int
	err = tx.QueryRow(ctx, `SELECT max_pending,per_minute FROM pilot.teams WHERE team=$1`, who.Team).Scan(&maxPending, &perMinute)
	if err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE a.status='running'),count(*) FILTER(WHERE p.submitted_at>clock_timestamp()-interval '1 minute') FROM pilot.runs p JOIN day12.agent_runs a USING(run_id) WHERE team=$1`, who.Team).Scan(&pending, &rate)
	if err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM day12.agent_runs WHERE queue_enabled AND status='running'`).Scan(&global)
	if err != nil {
		return "", false, err
	}
	if pending >= maxPending || rate >= perMinute || global >= s.MaxQueued {
		return "", false, errCapacity
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", false, err
	}
	id = hex.EncodeToString(random[:])
	history, _ := json.Marshal(map[string]string{"role": "user", "content": in.Goal})
	if in.Provider == "" {
		in.Provider = "coding:mock"
	}
	snap := durable.Snapshot{RunID: id, Repo: repo.Path, Provider: in.Provider, Goal: in.Goal, Status: durable.Running, History: []json.RawMessage{history}}
	if err = (durable.Queue{Pool: s.Pool, Pilot: true}).SubmitTx(ctx, tx, snap, 0); err != nil {
		return "", false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pilot.runs(run_id,principal,team,repository_id,source_commit,idempotency_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, who.Principal, who.Team, in.Repository, in.Commit, in.IdempotencyKey, digest)
	if err != nil {
		return "", false, err
	}
	return id, false, tx.Commit(ctx)
}
func (s *Server) cancel(ctx context.Context, id string, who Identity) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Same agent_runs row lock as Claim/Patch/FencedSave. A completed run remains completed.
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM day12.agent_runs WHERE run_id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return err
	}
	if status == "running" {
		_, err = tx.Exec(ctx, `UPDATE pilot.runs SET cancel_requested=true WHERE run_id=$1 AND principal=$2 AND team=$3`, id, who.Principal, who.Team)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE day12.agent_runs SET status='failed',failure_kind='cancelled',failure_reason='cancelled by authenticated owner',finished_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE run_id=$1 AND status='running'`, id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO day12.run_events(run_id,revision,lease_epoch,event_type,turns,calls,status) SELECT run_id,revision,lease_epoch,'cancelled',turns,calls,status FROM day12.agent_runs WHERE run_id=$1 ON CONFLICT DO NOTHING`, id)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
