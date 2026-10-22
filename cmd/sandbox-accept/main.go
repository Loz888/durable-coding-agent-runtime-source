// sandbox-accept requires a real local Docker engine and pinned, preloaded image.
// A missing prerequisite exits 2 with BLOCKED, never PASS or a skipped test.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/executor"
	"runtime-app/task"
	"strings"
	"time"
)

type observation struct {
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
}
type report struct {
	Status                   string        `json:"status"`
	Reason                   string        `json:"reason,omitempty"`
	Cases                    []observation `json:"cases"`
	OwnedContainersRemaining int           `json:"owned_containers_remaining"`
	Scope                    string        `json:"scope"`
}

func run() int {
	image := flag.String("image", "", "required pinned preloaded image@sha256:digest")
	output := flag.String("output", "", "required new private evidence file")
	orphan := flag.Bool("orphan-child", false, "internal isolated acceptance child")
	childRoot := flag.String("child-root", "", "internal child root")
	childSource := flag.String("child-source", "", "internal child source")
	flag.Parse()
	if *orphan {
		return orphanChild(*image, *childRoot, *childSource)
	}
	if *output == "" {
		fmt.Fprintln(os.Stderr, "new --output file required")
		return 2
	}
	file, e := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		fmt.Fprintln(os.Stderr, "output must be a new writable file")
		return 2
	}
	defer file.Close()
	out := report{Status: "BLOCKED", Scope: "real-container compile, confinement and deadline/cleanup acceptance; includes real Worker-SIGKILL scoped orphan reclamation; no hostile-kernel escape proof"}
	finish := func(code int) int {
		_ = json.NewEncoder(file).Encode(out)
		fmt.Printf("sandbox acceptance: %s\n", out.Status)
		return code
	}
	if _, e = exec.LookPath("docker"); e != nil {
		out.Reason = "Docker CLI/engine unavailable; install Docker Desktop and enable WSL integration"
		return finish(2)
	}
	root, e := os.MkdirTemp("", "runtime-sandbox-accept-")
	if e != nil {
		out.Reason = "cannot create owned temporary root"
		return finish(2)
	}
	defer os.RemoveAll(root)
	snapshotRoot := filepath.Join(root, "snapshots")
	if e = os.Mkdir(snapshotRoot, 0700); e != nil {
		out.Reason = "cannot create private snapshot root"
		return finish(2)
	}
	policy := executor.GoSandbox{Image: *image, Root: snapshotRoot, Timeout: 120 * time.Second, MaxBytes: 1 << 20, MaxFiles: 50}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if e = policy.Probe(ctx); e != nil {
		out.Reason = "working engine and an existing pinned local image are required; no pull or host fallback performed"
		return finish(2)
	}
	fixtures := []struct {
		name, source string
		timeout      time.Duration
		deadline     bool
	}{
		{"compile-positive", `package fixture
import "testing"
func TestPass(t *testing.T){if 2+2!=4{t.Fatal("arithmetic")}}
`, 120 * time.Second, false},
		{"confinement", `package fixture
import("testing";"os";"net";"strings";"strconv";"syscall")
func TestConfinement(t *testing.T){
 if os.Getuid()!=65532{t.Fatal("unexpected UID")}
 if os.WriteFile("/src/escape",[]byte("x"),0600)==nil{t.Fatal("candidate writable")}
 if os.WriteFile("/etc/runtime-escape",[]byte("x"),0600)==nil{t.Fatal("root filesystem writable")}
 interfaces,e:=net.Interfaces();if e!=nil{t.Fatal(e)};for _,i:=range interfaces{if i.Flags&net.FlagLoopback==0{t.Fatal("non-loopback network interface")}}
 conn,e:=net.DialTimeout("tcp","1.1.1.1:443",200000000);if e==nil{conn.Close();t.Fatal("network egress allowed")}
 status,e:=os.ReadFile("/proc/self/status");if e!=nil{t.Fatal(e)}
 seenCap,seenPrivileges:=false,false;for _,line:=range strings.Split(string(status),"\n"){fields:=strings.Fields(line);if len(fields)==2&&fields[0]=="CapEff:"{seenCap=true;if strings.Trim(fields[1],"0")!=""{t.Fatal("effective capabilities")}};if len(fields)==2&&fields[0]=="NoNewPrivs:"{seenPrivileges=true;if fields[1]!="1"{t.Fatal("no-new-privileges disabled")}}}
 if !seenCap||!seenPrivileges{t.Fatal("missing process isolation metadata")};cpu,e:=os.ReadFile("/sys/fs/cgroup/cpu.max");if e!=nil{t.Fatal(e)};parts:=strings.Fields(string(cpu));if len(parts)!=2{t.Fatal("CPU quota missing")};quota,e:=strconv.ParseInt(parts[0],10,64);if e!=nil{t.Fatal("CPU quota unlimited")};period,e:=strconv.ParseInt(parts[1],10,64);if e!=nil||quota<=0||period<=0||quota>period{t.Fatal("CPU quota unsafe")};for name,maximum:=range map[string]int64{"memory.max":536870912,"pids.max":64}{raw,e:=os.ReadFile("/sys/fs/cgroup/"+name);if e!=nil{t.Fatal("cgroup v2 required",e)};n,e:=strconv.ParseInt(strings.TrimSpace(string(raw)),10,64);if e!=nil||n<=0||n>maximum{t.Fatal("resource limit",name)}}
 var limit syscall.Rlimit;if syscall.Getrlimit(syscall.RLIMIT_NOFILE,&limit)!=nil||limit.Cur>128{t.Fatal("file descriptor limit")}
}
`, 120 * time.Second, false},
		{"execution-deadline", `package fixture
import("testing";"time")
func TestWait(t *testing.T){time.Sleep(30*time.Second)}
`, time.Second, true},
	}
	out.Status = "PASS"
	for _, fixture := range fixtures {
		source := filepath.Join(root, fixture.name)
		if e = os.Mkdir(source, 0700); e != nil {
			out.Status = "FAIL"
			out.Reason = "fixture setup failed"
			return finish(1)
		}
		for name, data := range map[string]string{"go.mod": "module example.invalid/sandboxaccept\n\ngo 1.23\n", "probe_test.go": fixture.source} {
			if e = os.WriteFile(filepath.Join(source, name), []byte(data), 0600); e != nil {
				out.Status = "FAIL"
				out.Reason = "fixture setup failed"
				return finish(1)
			}
		}
		execution := policy
		execution.Timeout = fixture.timeout
		started := time.Now()
		result, err := execution.Execute(ctx, task.Task{Command: "go-test", Payload: source})
		passed := err == nil && result.ExitCode == 0
		if fixture.deadline {
			passed = errors.Is(err, context.DeadlineExceeded)
		}
		out.Cases = append(out.Cases, observation{fixture.name, passed, result.ExitCode, time.Since(started).Milliseconds()})
		if !passed {
			out.Status = "FAIL"
		}
	}
	startedOrphan := time.Now()
	orphanErr := orphanAcceptance(ctx, *image, root)
	orphanExit := 0
	if orphanErr != nil {
		orphanExit = 1
		out.Status = "FAIL"
		out.Reason = "real orphan supervisor acceptance failed"
	}
	out.Cases = append(out.Cases, observation{"worker-sigkill-orphan-reap", orphanErr == nil, orphanExit, time.Since(startedOrphan).Milliseconds()})
	// Inspect only this acceptance's root label; never enumerate/delete other runs.
	command := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=io.agent-runtime.root="+executor.HashRoot(snapshotRoot))
	raw, e := command.Output()
	if e != nil {
		out.Status = "FAIL"
		out.Reason = "cleanup inspection unavailable"
	} else {
		out.OwnedContainersRemaining = len(strings.Fields(string(raw)))
		if out.OwnedContainersRemaining != 0 {
			out.Status = "FAIL"
			out.Reason = "owned container cleanup incomplete; inspect this run only"
		}
	}
	if out.Status != "PASS" {
		return finish(1)
	}
	return finish(0)
}
func main() { os.Exit(run()) }
