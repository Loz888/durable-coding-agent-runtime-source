package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"runtime-app/repodiag"
)

// Runner 管理 Agent Run 的检查点和恢复。
type Runner struct {
	Store     Store
	Model     repodiag.Model
	Workspace *repodiag.Workspace

	Provider string

	MaxTurns int
	MaxCalls int

	Guard func(context.Context) error
	// AfterSave 仅用于故障注入和实验观察。
	// 此回调发生在持久化成功之后。
	AfterSave func(Snapshot) error
}

func (r *Runner) Run(
	ctx context.Context,
	runID string,
	repo string,
	goal string,
) (Snapshot, error) {

	if r.Store == nil ||
		r.Model == nil ||
		r.Workspace == nil ||
		r.Provider == "" {

		return Snapshot{}, errors.New(
			"store, model, workspace, provider are required",
		)
	}

	// 先尝试恢复已有 Run。
	s, err := r.Store.Load(ctx, runID)

	if errors.Is(err, ErrNotFound) {

		raw, e := json.Marshal(
			map[string]string{
				"role":    "user",
				"content": goal,
			},
		)
		if e != nil {
			return Snapshot{}, e
		}

		s = Snapshot{
			RunID:    runID,
			Repo:     repo,
			Provider: r.Provider,
			Goal:     goal,
			Status:   Running,
			History:  []json.RawMessage{raw},
		}

		if err := s.Validate(); err != nil {
			return Snapshot{}, err
		}

		e = r.Store.Create(ctx, s)

		if e != nil && !errors.Is(e, ErrExists) {
			return Snapshot{}, e
		}

		s, err = r.Store.Load(ctx, runID)
	}

	if err != nil {
		return Snapshot{}, err
	}

	// 防止同一个 RunID 恢复到不同任务或模型。
	if s.Repo != repo ||
		s.Goal != goal ||
		s.Provider != r.Provider {

		return s, errors.New(
			"run_id already bound to a different repo, provider or goal",
		)
	}

	// 已经结束的 Run 不会重新执行。
	if s.Status != Running {
		return s, nil
	}

	maxTurns := r.MaxTurns
	maxCalls := r.MaxCalls

	if maxTurns <= 0 {
		maxTurns = 8
	}

	if maxCalls <= 0 {
		maxCalls = 12
	}

	for {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		if r.Guard != nil {
			if err := r.Guard(ctx); err != nil {
				return s, err
			}
		}
		// 优先处理已经持久化但尚未得到结果的 ToolCall。
		waiting, err := pendingCalls(s.History)

		if err != nil {
			return s, err
		}

		if len(waiting) > 0 {

			call := waiting[0]

			result := r.Workspace.Execute(
				ctx,
				call.Name,
				call.Arguments,
			)

			if err := ctx.Err(); err != nil {
				return s, err
			}

			outputBytes, _ := json.Marshal(
				map[string]any{
					"output":   result.Output,
					"is_error": result.IsError,
				},
			)

			item, _ := json.Marshal(
				map[string]string{
					"type":    "function_call_output",
					"call_id": call.CallID,
					"output":  string(outputBytes),
				},
			)

			next := s

			next.History = append(
				append(
					[]json.RawMessage{},
					s.History...,
				),
				item,
			)

			next.Revision++

			if err := r.save(
				ctx,
				next,
				s.Revision,
			); err != nil {
				return s, err
			}

			s = next
			continue
		}

		// 当前没有等待执行的 ToolCall。
		// 可以继续调用模型。
		if s.Turns >= maxTurns {
			return r.fail(
				ctx,
				s,
				ErrTurnLimit,
			)
		}

		turn, err := r.Model.Next(
			ctx,
			s.History,
		)

		if err != nil {
			return s, err
		}

		if err := ctx.Err(); err != nil {
			return s, err
		}

		// 没有 ToolCall：产生最终报告。
		if len(turn.Calls) == 0 {

			if strings.TrimSpace(turn.Text) == "" {
				return s, errors.New(
					"model gave neither tool call nor final text",
				)
			}

			next := s

			next.Status = Succeeded
			next.Final = turn.Text
			next.Turns++
			next.Revision++

			if err := r.save(
				ctx,
				next,
				s.Revision,
			); err != nil {
				return s, err
			}

			return next, nil
		}

		// 检查整个 Run 的工具调用数量。
		if s.Calls+len(turn.Calls) > maxCalls {
			return r.fail(
				ctx,
				s,
				ErrCallLimit,
			)
		}

		// 目前只支持 Day 11 的简化 FunctionCall
		// History，不支持完整 reasoning items。
		if err := validateTurn(
			s.History,
			turn,
		); err != nil {
			return s, err
		}

		// 先保存模型决策，再执行 Tool。
		next := s

		next.History = append(
			append(
				[]json.RawMessage{},
				s.History...,
			),
			turn.Items...,
		)

		next.Revision++
		next.Turns++
		next.Calls += len(turn.Calls)

		if err := r.save(
			ctx,
			next,
			s.Revision,
		); err != nil {
			return s, err
		}

		s = next
	}
}

// save 保证先提交 Checkpoint，
// 再执行故障注入 Hook。
func (r *Runner) save(
	ctx context.Context,
	s Snapshot,
	previous int64,
) error {

	if err := r.Store.Save(
		ctx,
		s,
		previous,
	); err != nil {
		return err
	}

	if r.AfterSave != nil {
		return r.AfterSave(s)
	}

	return nil
}

func (r *Runner) fail(
	ctx context.Context,
	s Snapshot,
	reason error,
) (Snapshot, error) {

	next := s

	next.Status = Failed
	next.Final = reason.Error()
	next.Revision++

	if err := r.save(
		ctx,
		next,
		s.Revision,
	); err != nil {
		return s, err
	}

	return next, reason
}

// pendingCalls 根据 History 找出：
// 有 FunctionCall，但还没有 FunctionCallOutput 的调用。
func pendingCalls(
	history []json.RawMessage,
) ([]repodiag.ToolCall, error) {

	var calls []repodiag.ToolCall

	known := make(map[string]bool)
	completed := make(map[string]bool)

	for _, raw := range history {

		var tag struct {
			Type string `json:"type"`
		}

		if err := json.Unmarshal(
			raw,
			&tag,
		); err != nil {
			return nil, err
		}

		switch tag.Type {

		case "function_call":

			var item struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}

			if err := json.Unmarshal(
				raw,
				&item,
			); err != nil {
				return nil, err
			}

			if item.CallID == "" ||
				item.Name == "" ||
				!json.Valid(
					[]byte(item.Arguments),
				) ||
				known[item.CallID] {

				return nil, errors.New(
					"invalid or duplicate tool call",
				)
			}

			known[item.CallID] = true

			calls = append(
				calls,
				repodiag.ToolCall{
					CallID: item.CallID,
					Name:   item.Name,
					Arguments: json.RawMessage(
						item.Arguments,
					),
				},
			)

		case "function_call_output":

			var item struct {
				CallID string `json:"call_id"`
			}

			if err := json.Unmarshal(
				raw,
				&item,
			); err != nil {
				return nil, err
			}

			if !known[item.CallID] ||
				completed[item.CallID] {

				return nil, errors.New(
					"orphan or duplicate tool result",
				)
			}

			completed[item.CallID] = true

		case "":
			var user struct {
				Role string `json:"role"`
			}

			_ = json.Unmarshal(raw, &user)

			if user.Role != "user" {
				return nil, errors.New(
					"unsupported history item",
				)
			}

		default:
			return nil, fmt.Errorf(
				"unsupported history type %q",
				tag.Type,
			)
		}
	}

	pending := make(
		[]repodiag.ToolCall,
		0,
	)

	for _, c := range calls {
		if !completed[c.CallID] {
			pending = append(
				pending,
				c,
			)
		}
	}

	return pending, nil
}

// validateTurn 校验 Model.Next() 返回的
// ToolCall 和 RawMessage 是否一一对应。
func validateTurn(
	history []json.RawMessage,
	turn repodiag.Turn,
) error {

	if len(turn.Calls) != len(turn.Items) {
		return errors.New(
			"model turn must contain only tool call items",
		)
	}

	known := make(map[string]bool)

	for _, raw := range history {

		var item struct {
			CallID string `json:"call_id"`
			Type   string `json:"type"`
		}

		_ = json.Unmarshal(raw, &item)

		if item.Type == "function_call" {
			known[item.CallID] = true
		}
	}

	for i, call := range turn.Calls {

		var item struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		}

		if err := json.Unmarshal(
			turn.Items[i],
			&item,
		); err != nil {
			return err
		}

		if item.Type != "function_call" ||
			call.CallID == "" ||
			known[call.CallID] ||
			item.CallID != call.CallID ||
			item.Name != call.Name ||
			!json.Valid(call.Arguments) ||
			!bytes.Equal(
				bytes.TrimSpace(
					[]byte(item.Arguments),
				),
				bytes.TrimSpace(
					call.Arguments,
				),
			) {

			return errors.New(
				"model turn/tool call mismatch or duplicate call_id",
			)
		}

		known[call.CallID] = true
	}

	return nil
}
