//go:build dockeraccept

package integration20

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/artifactstore"
	"runtime-app/durable"
	"runtime-app/repair17"
	"runtime-app/repair19"
	"strings"
	"testing"
	"time"
)

type hostConfig struct {
	Job                               durable.ClaimedRun
	Schema, Source, Base, ArtifactURL string
	Prepare                           bool
}

func TestPortableContainerHost(t *testing.T) {
	raw := os.Getenv("PORTABLE_HOST_CONFIG")
	if raw == "" {
		return
	}
	var c hostConfig
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("bad private configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, os.Getenv("DAY12_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal("test DB unavailable")
	}
	defer pool.Close()
	var db string
	if e = pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); e != nil || db != "agent_runtime_day14_test" {
		t.Fatal("not test DB")
	}
	store, e := repair19.NewStore(pool, "day12", c.Schema)
	if e != nil {
		t.Fatal(e)
	}
	store.Artifacts, e = artifactstore.NewRemote(c.ArtifactURL, os.Getenv("ARTIFACT_TOKEN"), true)
	if e != nil {
		t.Fatal(e)
	}
	if c.Prepare {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, e := (durable.LeaseManager{Pool: pool}).Renew(ctx, c.Job.Lease, 5*time.Second); e != nil {
						return
					}
				}
			}
		}()
		w, e := repair17.Prepare(ctx, c.Source, c.Base, c.Job.RunID)
		if e != nil {
			t.Fatal(e)
		}
		r := repair19.Ref{ID: c.Job.RunID, Owner: c.Job.Lease.Owner, Epoch: c.Job.Lease.Epoch}
		if e = store.Register(ctx, r, w.Path, 2); e != nil {
			t.Fatal(e)
		}
		if _, e = store.Prepare(ctx, r, "repair-1", patchFor(t, w)); e != nil {
			t.Fatal(e)
		}
		if _, e = store.ApplyOrRecover(ctx, r, "repair-1", nil); e != nil {
			t.Fatal(e)
		}
		if v, e := store.Verify(ctx, r, "repair-1", 15*time.Second); e != nil || !v.Passed {
			t.Fatal(v, e)
		}
		if _, e = store.PublishPortable(ctx, r); e != nil {
			t.Fatal(e)
		}
		fmt.Println("PORTABLE_HOST_A_COMMITTED")
		select {}
	}
	h := durable.CodingHandler{Pool: pool, PatchStore: store, PatchSchema: c.Schema, WorktreeBase: c.Base, File: "math.go", MaxRounds: 2, TestTimeout: 15 * time.Second, Proposer: noMultiModel{}, ResolveRepo: func(context.Context, durable.ClaimedRun) (string, error) { return c.Source, nil }}
	snap, e := h.Execute(ctx, c.Job)
	if e != nil || snap.Status != durable.Succeeded {
		t.Fatal(snap, e)
	}
	fmt.Println("PORTABLE_HOST_B_RESTORED")
}
func TestPortableIsolatedContainerHosts(t *testing.T) {
	image := os.Getenv("RUNTIME_SANDBOX_IMAGE")
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("pinned RUNTIME_SANDBOX_IMAGE required for dockeraccept build tag")
	}
	s := newPatchLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	_, fixture := makeWorktree(t, "portable-container-baseline")
	root := t.TempDir()
	aRoot := filepath.Join(root, "host-a")
	bRoot := filepath.Join(root, "host-b")
	for _, r := range []string{aRoot, bRoot} {
		if e := os.Mkdir(r, 0700); e != nil {
			t.Fatal(e)
		}
		runGit(t, fixture, "clone", "--no-hardlinks", "--quiet", fixture, filepath.Join(r, "source"))
	}
	id := "containers-" + strings.TrimPrefix(s.schema, "integration20_")
	q := durable.Queue{Pool: s.Pool}
	if e := q.Submit(ctx, durable.Snapshot{RunID: id, Repo: "logical-repository-fixture", Provider: "coding:mock", Goal: "isolated host restoration", Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"synthetic recovery"}`)}}, 0); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := s.Pool.Exec(context.Background(), "DELETE FROM day12.agent_runs WHERE run_id=$1", id); e != nil {
			t.Error(e)
		}
	})
	// Prepare infrastructure before the short initial claim.
	network := "portable-" + strings.TrimPrefix(s.schema, "integration20_")
	label := "runtime.portable.lab=" + network
	docker := func(args ...string) (string, error) {
		b, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.TrimSpace(string(b)), e
	}
	if _, e := docker("network", "create", "--internal", "--label", label, network); e != nil {
		t.Fatal("private network create failed")
	}
	var containers []string
	defer func() {
		for _, name := range containers {
			c, stop := context.WithTimeout(context.Background(), 10*time.Second)
			_ = exec.CommandContext(c, "docker", "rm", "-f", name).Run()
			stop()
		}
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(c, "docker", "network", "rm", network).Run()
	}()
	serverBinary := filepath.Join(root, "artifact-server")
	build := exec.CommandContext(ctx, "go", "build", "-o", serverBinary, "./cmd/artifact-server")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatal("server build", string(out))
	}
	artifactRoot := filepath.Join(root, "artifacts")
	if _, e := artifactstore.NewDisk(artifactRoot); e != nil {
		t.Fatal(e)
	}
	token := strings.TrimPrefix(s.schema, "integration20_") + "-artifact-private"
	artifactName := network + "-artifact"
	containers = append(containers, artifactName)
	uid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if _, e := docker("run", "-d", "--name", artifactName, "--label", label, "--network", network, "--network-alias", "artifacts", "--user", uid, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--mount", "type=bind,source="+serverBinary+",target=/artifact-server,readonly", "--mount", "type=bind,source="+artifactRoot+",target=/artifacts", "--env", "ARTIFACT_TOKEN="+token, image, "/artifact-server", "-listen", "0.0.0.0:8095", "-root", "/artifacts"); e != nil {
		t.Fatal("artifact service failed")
	}
	pgConfig, e := pgxpool.ParseConfig(s.dsn)
	if e != nil || !filepath.IsAbs(pgConfig.ConnConfig.Host) {
		t.Fatal("owned Unix-socket PG required")
	}
	socket := pgConfig.ConnConfig.Host
	pgConfig.ConnConfig.Host = "/db-socket"
	u := url.URL{Scheme: "postgres", Path: "/agent_runtime_day14_test", RawQuery: url.Values{"host": []string{"/db-socket"}, "port": []string{fmt.Sprint(pgConfig.ConnConfig.Port)}, "user": []string{pgConfig.ConnConfig.User}, "sslmode": []string{"disable"}}.Encode()}
	containerDSN := u.String()
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	hostArgs := func(name, root, mount string, c hostConfig) []string {
		raw, _ := json.Marshal(c)
		return []string{"run", "-d", "--name", name, "--label", label, "--network", network, "--user", uid, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", "128", "--memory", "1g", "--cpus", "2", "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=256m", "--mount", "type=bind,source=" + executable + ",target=/accept,readonly", "--mount", "type=bind,source=" + root + ",target=" + mount, "--mount", "type=bind,source=" + socket + ",target=/db-socket", "--env", "DAY12_TEST_DATABASE_URL=" + containerDSN, "--env", "ARTIFACT_TOKEN=" + token, "--env", "PORTABLE_HOST_CONFIG=" + string(raw), "--env", "HOME=/tmp", "--env", "GOCACHE=/tmp/cache", "--env", "GOPATH=/tmp/gopath", image, "/accept", "-test.run=^TestPortableContainerHost$", "-test.v", "-test.timeout=60s"}
	}
	jobs, e := q.ClaimBatch(ctx, "container-A", 1, 5*time.Second)
	if e != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatal("initial real ClaimBatch", e)
	}
	job := jobs[0]
	aName := network + "-a"
	containers = append(containers, aName)
	config := hostConfig{job, s.schema, "/host-a/source", "/host-a/worktrees", "http://artifacts:8095", true}
	if _, e = docker(hostArgs(aName, aRoot, "/host-a", config)...); e != nil {
		t.Fatal("host A start")
	}
	deadline := time.Now().Add(35 * time.Second)
	for {
		out, _ := docker("logs", aName)
		if strings.Contains(out, "PORTABLE_HOST_A_COMMITTED") {
			break
		}
		if time.Now().After(deadline) || strings.Contains(out, "FAIL") {
			t.Fatal("host A boundary failed", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, e = docker("kill", "--signal", "KILL", aName); e != nil {
		t.Fatal(e)
	}
	exit, e := docker("inspect", "--format", "{{.State.ExitCode}}", aName)
	if e != nil || exit != "137" {
		t.Fatal("container not SIGKILL", exit, e)
	}
	deadline = time.Now().Add(12 * time.Second)
	for {
		jobs, e = q.ClaimBatch(ctx, "container-B", 1, 30*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if len(jobs) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("natural expiry takeover timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(jobs) != 1 || jobs[0].RunID != id || jobs[0].Lease.Epoch != job.Lease.Epoch+1 {
		t.Fatal("bad real takeover")
	}
	if e = (durable.LeaseManager{Pool: s.Pool}).Check(ctx, job.Lease); !errors.Is(e, durable.ErrLeaseLost) {
		t.Fatal("old host not fenced", e)
	}
	bName := network + "-b"
	containers = append(containers, bName)
	config = hostConfig{jobs[0], s.schema, "/host-b/source", "/host-b/worktrees", "http://artifacts:8095", false}
	if _, e = docker(hostArgs(bName, bRoot, "/host-b", config)...); e != nil {
		t.Fatal("host B start")
	}
	exit, e = docker("wait", bName)
	out, _ := docker("logs", bName)
	if e != nil || exit != "0" || !strings.Contains(out, "PORTABLE_HOST_B_RESTORED") {
		t.Fatal("host B failed", exit, out)
	}
	snap, e := (durable.PostgresStore{Pool: s.Pool}).Load(ctx, id)
	if e != nil || snap.Status != durable.Succeeded {
		t.Fatal(snap, e)
	}
	for _, r := range []string{aRoot, bRoot} {
		raw, e := exec.Command("git", "-C", filepath.Join(r, "source"), "status", "--porcelain").Output()
		if e != nil || len(raw) != 0 {
			t.Fatal("baseline clone modified", e)
		}
	}
	t.Log("PASS: host A SIGKILL exit137; separate A/B filesystems; independent authenticated artifact service; natural lease expiry and real Epoch2 ClaimBatch; no model; B retested and committed; both clones clean. One physical computer only.")
}
