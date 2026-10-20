package repodiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ToolCall struct {
	CallID    string
	Name      string
	Arguments json.RawMessage
}

type Turn struct {
	Items     []json.RawMessage
	Calls     []ToolCall
	Text      string
	Diagnosis *DiagnosticReport
}

// 模型接口：真实模型与假模型统一实现。
type Model interface {
	Next(
		ctx context.Context,
		history []json.RawMessage,
	) (Turn, error)
}

type Agent struct {
	Model     Model
	Workspace *Workspace

	MaxTurns int
	MaxCalls int
}

type Report struct {
	Goal           string            `json:"goal"`
	Final          string            `json:"final"`
	Status         string            `json:"status"`
	OutcomeVerdict string            `json:"outcome_verdict"`
	Diagnosis      *DiagnosticReport `json:"diagnosis,omitempty"`
	Steps          []ToolResult      `json:"steps"`
}

var ErrStepLimit = errors.New(
	"agent turn limit reached",
)

func (a *Agent) Run(
	ctx context.Context,
	goal string,
) (Report, error) {
	report := Report{
		Goal:   goal,
		Status: "running",
		Steps:  []ToolResult{},
	}

	if a.Model == nil || a.Workspace == nil {
		return report, errors.New(
			"model and workspace are required",
		)
	}

	if strings.TrimSpace(goal) == "" {
		return report, errors.New(
			"goal is empty",
		)
	}

	turns := a.MaxTurns
	if turns <= 0 {
		turns = 8
	}

	callsLimit := a.MaxCalls
	if callsLimit <= 0 {
		callsLimit = 12
	}

	first, _ := json.Marshal(
		map[string]string{
			"role":    "user",
			"content": goal,
		},
	)

	history := []json.RawMessage{first}
	totalCalls := 0

	for i := 0; i < turns; i++ {

		if err := ctx.Err(); err != nil {
			report.Status = "cancelled"
			return report, err
		}

		turn, err := a.Model.Next(ctx, history)
		if err != nil {
			report.Status = "failed"
			return report, err
		}

		// 保留模型原始输出。
		// 可能包含 function_call、message、reasoning。
		history = append(history, turn.Items...)

		// 没有 ToolCall：本轮应输出最终报告。
		if len(turn.Calls) == 0 {

			if strings.TrimSpace(turn.Text) == "" {
				report.Status = "failed"
				return report, errors.New(
					"model gave neither call nor final report",
				)
			}

			report.Final = turn.Text
			report.Status = "succeeded"
			report.Diagnosis = turn.Diagnosis
			report.OutcomeVerdict = VerifyDiagnostic(turn.Diagnosis, history)

			return report, nil
		}

		// 防止某次模型响应产生过多 ToolCall。
		if totalCalls+len(turn.Calls) > callsLimit {
			report.Status = "failed"
			return report, fmt.Errorf(
				"tool call limit reached (%d)",
				callsLimit,
			)
		}

		for _, call := range turn.Calls {

			if call.CallID == "" {
				report.Status = "failed"
				return report, errors.New(
					"tool call missing call_id",
				)
			}

			// 所有工具都由受限 Workspace 执行。
			result := a.Workspace.Execute(
				ctx,
				call.Name,
				call.Arguments,
			)

			report.Steps = append(
				report.Steps,
				result,
			)

			totalCalls++

			output := result.OutputEnvelope()

			// 用原始 call_id 配对调用结果。
			item, _ := json.Marshal(
				map[string]string{
					"type":    "function_call_output",
					"call_id": call.CallID,
					"output":  output,
				},
			)

			history = append(history, item)
		}
	}

	report.Status = "failed"

	return report, ErrStepLimit
}

// DemoModel 是确定性的离线教学模型。
// 注意：它不是 LLM，不会智能诊断代码。
type DemoModel struct{}

func (DemoModel) Next(
	ctx context.Context,
	history []json.RawMessage,
) (Turn, error) {
	count := 0

	for _, raw := range history {
		var v struct {
			Type string `json:"type"`
		}

		_ = json.Unmarshal(raw, &v)

		if v.Type == "function_call_output" {
			count++
		}
	}

	names := []string{
		"list_files",
		"run_go_tests",
		"read_file",
	}

	args := []string{
		"{}",
		"{}",
		`{"path":"go.mod"}`,
	}

	if count >= len(names) {
		txt := "离线演示完成：已调用 " +
			"list_files、run_go_tests、read_file。" +
			"此报告不是模型诊断；" +
			"请检查 steps 中的真实测试日志。"

		raw, _ := json.Marshal(
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]string{
					{
						"type": "output_text",
						"text": txt,
					},
				},
			},
		)

		return Turn{
			Items: []json.RawMessage{raw},
			Text:  txt,
		}, nil
	}

	raw, _ := json.Marshal(
		map[string]any{
			"type": "function_call",
			"name": names[count],
			"call_id": fmt.Sprintf(
				"demo-%d",
				count,
			),
			"arguments": args[count],
		},
	)

	return Turn{
		Items: []json.RawMessage{raw},
		Calls: []ToolCall{
			{
				CallID: fmt.Sprintf(
					"demo-%d",
					count,
				),
				Name: names[count],
				Arguments: json.RawMessage(
					args[count],
				),
			},
		},
	}, nil
}
