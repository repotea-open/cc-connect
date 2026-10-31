package core

import "testing"

func TestExecuteCronShell_SilentMarkerSkipsDelivery(t *testing.T) {
	tests := []struct {
		name     string
		exec     string
		wantSent int
	}{
		{"silent marker", "echo '[SILENT]'", 0},
		{"real output", "echo hello", 1},
		{"marker among other text", "printf 'a\\n[SILENT]'", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEngine()
			p := &stubPlatformEngine{n: "stub"}
			job := &CronJob{ID: "j", Exec: tt.exec}
			if err := e.executeCronShell(p, "ctx", job); err != nil {
				t.Fatal(err)
			}
			if got := len(p.getSent()); got != tt.wantSent {
				t.Fatalf("sent %d messages (%v), want %d", got, p.getSent(), tt.wantSent)
			}
		})
	}
}
