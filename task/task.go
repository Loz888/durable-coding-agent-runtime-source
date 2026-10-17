package task

type Task struct {
	ID        int
	Name      string
	Payload   string
	ToolName  string
	Command   string
	Args      []string
	RetrySafe bool
}

func New(
	id int,
	name string,
	payload string,
) Task {
	return Task{
		ID:      id,
		Name:    name,
		Payload: payload,
	}
}
