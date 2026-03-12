package agent

// AllWriters returns all supported AgentWriters.
func AllWriters() []AgentWriter {
	return []AgentWriter{
		NewClaudeCodeWriter(),
		NewCursorWriter(),
		NewWindsurfWriter(),
		NewVSCodeWriter(),
	}
}

// DetectedWriters returns only the writers whose Detect() method returns true,
// indicating that the corresponding tool appears to be installed on this machine.
func DetectedWriters() []AgentWriter {
	var detected []AgentWriter
	for _, w := range AllWriters() {
		if w.Detect() {
			detected = append(detected, w)
		}
	}
	return detected
}

// WriterByName returns the AgentWriter with the given canonical name, or nil
// if no writer matches.
func WriterByName(name string) AgentWriter {
	for _, w := range AllWriters() {
		if w.Name() == name {
			return w
		}
	}
	return nil
}
