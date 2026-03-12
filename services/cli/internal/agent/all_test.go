package agent

import "testing"

func TestWriterByName(t *testing.T) {
	tests := []struct {
		name   string
		expect string
	}{
		{"claude-code", "claude-code"},
		{"cursor", "cursor"},
		{"windsurf", "windsurf"},
		{"vscode", "vscode"},
	}
	for _, tt := range tests {
		w := WriterByName(tt.name)
		if w == nil {
			t.Fatalf("WriterByName(%q) returned nil", tt.name)
		}
		if w.Name() != tt.expect {
			t.Fatalf("WriterByName(%q).Name() = %q, want %q", tt.name, w.Name(), tt.expect)
		}
	}

	// Unknown name should return nil.
	if w := WriterByName("nonexistent"); w != nil {
		t.Fatalf("WriterByName(nonexistent) should be nil, got %v", w.Name())
	}
}
