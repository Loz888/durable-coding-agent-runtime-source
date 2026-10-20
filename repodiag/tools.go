package repodiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Workspace 仅用于用户明确授权的可信本地仓库。
type Workspace struct {
	root string
}

func NewWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}

	s, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}

	if !s.IsDir() {
		return nil, errors.New(
			"repository root must be a directory",
		)
	}

	if _, err := os.Stat(
		filepath.Join(abs, "go.mod"),
	); err != nil {
		return nil, fmt.Errorf(
			"not a Go module: %w",
			err,
		)
	}

	return &Workspace{root: abs}, nil
}

type ToolResult struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
	IsError   bool   `json:"is_error"`
	Executed  bool   `json:"executed"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	TimedOut  bool   `json:"timed_out"`
}

func (w *Workspace) Execute(
	ctx context.Context,
	name string,
	args json.RawMessage,
) ToolResult {
	out := ToolResult{
		Name:      name,
		Arguments: string(args),
	}

	fail := func(err error) ToolResult {
		out.IsError = true
		out.Output = err.Error()
		return out
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	switch name {

	case "list_files":
		if err := noArgs(args); err != nil {
			return fail(err)
		}

		paths := make([]string, 0, 50)

		err := filepath.WalkDir(
			w.root,
			func(
				path string,
				entry os.DirEntry,
				walkErr error,
			) error {
				if walkErr != nil {
					return walkErr
				}

				if path == w.root {
					return nil
				}

				// 不跟随符号链接。
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}

				// 不遍历常见依赖和元数据目录。
				if entry.IsDir() &&
					(entry.Name() == ".git" ||
						entry.Name() == "vendor" ||
						entry.Name() == "node_modules" ||
						entry.Name() == ".cache") {
					return filepath.SkipDir
				}

				if entry.IsDir() {
					return nil
				}

				rel, err := filepath.Rel(w.root, path)
				if err != nil {
					return err
				}

				if !allowedPath(rel) {
					return nil
				}

				paths = append(
					paths,
					filepath.ToSlash(rel),
				)

				if len(paths) >= 60 {
					return io.EOF
				}

				return nil
			},
		)

		if err != nil && !errors.Is(err, io.EOF) {
			return fail(err)
		}

		b, _ := json.Marshal(paths)
		out.Output = string(b)
		return out

	case "read_file":
		var a struct {
			Path string `json:"path"`
		}

		if err := parseArgs(args, &a); err != nil {
			return fail(err)
		}

		if !allowedPath(a.Path) ||
			!filepath.IsLocal(a.Path) {
			return fail(errors.New("path not allowed"))
		}

		joined := filepath.Join(w.root, a.Path)

		real, err := filepath.EvalSymlinks(joined)
		if err != nil {
			return fail(err)
		}

		rel, err := filepath.Rel(w.root, real)

		if err != nil ||
			!filepath.IsLocal(rel) ||
			!allowedPath(rel) {
			return fail(errors.New(
				"path escapes allowed repository",
			))
		}

		f, err := os.Open(real)
		if err != nil {
			return fail(err)
		}
		defer f.Close()

		stat, err := f.Stat()
		if err != nil {
			return fail(err)
		}

		if !stat.Mode().IsRegular() {
			return fail(errors.New(
				"not a regular file",
			))
		}

		data, err := io.ReadAll(
			io.LimitReader(f, 16*1024+1),
		)
		if err != nil {
			return fail(err)
		}

		if len(data) > 16*1024 {
			return fail(errors.New(
				"file exceeds 16 KiB limit",
			))
		}

		out.Output = string(data)
		return out

	case "run_go_tests":
		if err := noArgs(args); err != nil {
			return fail(err)
		}

		runCtx, cancel := context.WithTimeout(
			ctx,
			20*time.Second,
		)
		defer cancel()

		// 固定命令，不让模型控制任意 Shell 参数。
		cmd := exec.CommandContext(
			runCtx,
			"go",
			"test",
			"-count=1",
			"./...",
		)

		cmd.Dir = w.root
		cmd.WaitDelay = 2 * time.Second

		// 限制传给测试进程的环境变量。
		// 禁用 Go 模块与工具链的自动网络下载。
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"GOPROXY=off",
			"GOSUMDB=off",
			"GOTOOLCHAIN=local",
			"GOWORK=off",
			"GOFLAGS=",
		}

		buffer := &cappedBuffer{
			limit: 12 * 1024,
		}

		cmd.Stdout = buffer
		cmd.Stderr = buffer

		err := cmd.Run()
		if err == nil {
			code := 0
			out.Executed = true
			out.ExitCode = &code
		} else {
			var exited *exec.ExitError
			if errors.As(err, &exited) {
				code := exited.ExitCode()
				out.Executed = true
				out.ExitCode = &code
			}
		}
		out.TimedOut = runCtx.Err() != nil

		out.Output = buffer.String()

		if err != nil {
			out.IsError = true
			out.Output += "\n[go test error: " +
				err.Error() + "]"
		}

		if runCtx.Err() != nil {
			out.IsError = true
			out.Output += "\n[context: " +
				runCtx.Err().Error() + "]"
		}

		return out

	default:
		return fail(fmt.Errorf(
			"unknown tool %q",
			name,
		))
	}
}

func noArgs(raw json.RawMessage) error {
	var m map[string]json.RawMessage

	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}

	if len(m) != 0 {
		return errors.New(
			"tool takes no arguments",
		)
	}

	return nil
}

func parseArgs(
	raw json.RawMessage,
	v any,
) error {
	d := json.NewDecoder(
		strings.NewReader(string(raw)),
	)

	d.DisallowUnknownFields()

	if err := d.Decode(v); err != nil {
		return err
	}

	var more any
	if err := d.Decode(&more); err != io.EOF {
		return errors.New(
			"unexpected trailing JSON",
		)
	}

	return nil
}

func allowedPath(p string) bool {
	if !filepath.IsLocal(p) {
		return false
	}

	for _, s := range strings.Split(
		filepath.ToSlash(p),
		"/",
	) {
		if strings.HasPrefix(s, ".") ||
			strings.Contains(
				strings.ToLower(s),
				"secret",
			) ||
			strings.Contains(
				strings.ToLower(s),
				"credential",
			) ||
			strings.Contains(
				strings.ToLower(s),
				"key",
			) {
			return false
		}
	}

	switch strings.ToLower(filepath.Ext(p)) {
	case ".go", ".mod", ".md", ".txt":
		return true
	}

	return false
}

// cappedBuffer 防止测试输出无限占用内存。
type cappedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(
	p []byte,
) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	orig := len(p)
	left := b.limit - len(b.data)

	if left > 0 {
		n := left
		if n > len(p) {
			n = len(p)
		}

		b.data = append(b.data, p[:n]...)
	}

	if orig > left {
		b.truncated = true
	}

	return orig, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := string(b.data)

	if b.truncated {
		s += "\n[output truncated]"
	}

	return s
}
