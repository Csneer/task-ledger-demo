package core

const Version = "task-core-draft-1"
const MaxInteger int64 = 9007199254740991

type Object = map[string]any

type ActorRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type TaskRef struct {
	AuthorityID string `json:"authority_id"`
	TaskID      string `json:"task_id"`
}

type Command struct {
	ProtocolVersion  string  `json:"protocol_version"`
	CommandID        string  `json:"command_id"`
	Task             TaskRef `json:"task"`
	ExpectedRevision int64   `json:"expected_revision"`
	Operation        string  `json:"operation"`
	Body             Object  `json:"body"`
}

type Record struct {
	RecordType string `json:"record_type"`
	Record     Object `json:"record"`
}

type Snapshot struct {
	Task    Object   `json:"task"`
	Records []Record `json:"records"`
}

type Result struct {
	ProtocolVersion string   `json:"protocol_version"`
	CommandID       string   `json:"command_id"`
	Replayed        bool     `json:"replayed"`
	Task            Object   `json:"task"`
	Records         []Record `json:"records"`
	Event           Object   `json:"event"`
}

type Error struct {
	Kind string
}

func (e *Error) Error() string { return e.Kind }
