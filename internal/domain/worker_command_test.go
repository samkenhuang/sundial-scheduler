package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestWorkerCommandMarshalsPayloadForWorkerPoll(t *testing.T) {
	cmd := WorkerCommand{
		ID:       uuid.New(),
		WorkerID: uuid.New(),
		Kind:     "start_log_stream",
		Payload:  json.RawMessage(`{"runId":"abc","follow":true}`),
	}
	body, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(decoded["payload"]) != `{"runId":"abc","follow":true}` {
		t.Fatalf("payload=%s", decoded["payload"])
	}
}
