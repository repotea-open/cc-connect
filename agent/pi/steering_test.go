package pi

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type steeringWriter struct{ bytes.Buffer }

func (*steeringWriter) Close() error { return nil }

var _ io.WriteCloser = (*steeringWriter)(nil)

func TestRPCPromptAlwaysUsesSteeringBehavior(t *testing.T) {
	w := &steeringWriter{}
	s := &piSession{rpc: true, rpcStdin: w, attachDir: t.TempDir()}
	s.alive.Store(true)
	if err := s.Send("initial", "one", nil, nil); err != nil {
		t.Fatal(err)
	}
	if handled, err := s.TrySteer("stop this direction", "two", nil, nil); !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	dec := json.NewDecoder(&w.Buffer)
	for _, expected := range []string{"initial", "stop this direction"} {
		var cmd map[string]any
		if err := dec.Decode(&cmd); err != nil {
			t.Fatal(err)
		}
		if cmd["type"] != "prompt" || cmd["streamingBehavior"] != "steer" || cmd["message"] != expected {
			t.Fatalf("unexpected command: %v", cmd)
		}
	}
}

func TestJSONSessionDoesNotClaimSteering(t *testing.T) {
	var s core.SteeringAgentSession = &piSession{}
	if handled, err := s.TrySteer("input", "", nil, nil); handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestTextSteerPreservesActiveAttachments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "active.txt")
	if err := os.WriteFile(file, []byte("in use"), 0600); err != nil {
		t.Fatal(err)
	}
	w := &steeringWriter{}
	s := &piSession{rpc: true, rpcStdin: w, attachDir: dir}
	s.alive.Store(true)
	if handled, err := s.TrySteer("new instruction", "", nil, nil); !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("steer removed an active attachment: %v", err)
	}
}
