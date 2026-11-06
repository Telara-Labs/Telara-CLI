package handoffrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// Telara refuses the first input to a provider thread that already carries
// earlier input outside this work's sensitivity envelope (TENG-3693 Q23). The
// refusal names a fresh session of the same agent as the way on, and no input
// was released, so the runner opens one new thread of the same Codex and asks
// once more. A second refusal ends the run: the runner never loops.

// freshSessionPhrase is the refusal's stable wording, from agent-service
// handoffRunnerStatus. It is matched rather than the status code, because the
// same code also means "this run already started".
const freshSessionPhrase = "start a fresh session"

// FreshSessionRequired reports whether err is Telara's refusal to give this
// thread the run's input.
func FreshSessionRequired(err error) bool {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return strings.Contains(apiErr.Message, freshSessionPhrase) || strings.Contains(apiErr.Body, freshSessionPhrase)
}

// inputThread is the agent thread the first input goes to.
type inputThread interface {
	attachment() api.HandoffRunAttachment
	// reopen starts a new thread of the same agent, attached to the same grant
	// session and proved before anything is sent.
	reopen() error
}

// takeFirstInput asks for the run's first input for the thread, and after a
// fresh-session refusal asks once more for one new thread.
func takeFirstInput(ctx context.Context, control Control, executionID, incarnationID string, thread inputThread, logf func(string, ...any)) (*api.HandoffRunInput, error) {
	input, err := control.GetHandoffRunInput(ctx, executionID, incarnationID, thread.attachment())
	if err == nil || !FreshSessionRequired(err) {
		return input, err
	}
	if logf != nil {
		logf("run %s: Telara asked for a fresh session of the agent; opening a new thread and asking once more", executionID)
	}
	if err := thread.reopen(); err != nil {
		return nil, fmt.Errorf("fresh thread: %w", err)
	}
	return control.GetHandoffRunInput(ctx, executionID, incarnationID, thread.attachment())
}

// codexThread is the run's Codex thread as takeFirstInput sees it.
type codexThread struct {
	agent                             *codex
	workdir, mcpURL, localAuthorizing string
}

func (t codexThread) attachment() api.HandoffRunAttachment {
	return api.HandoffRunAttachment{RuntimeVersion: t.agent.version, ThreadID: t.agent.thread, AttachmentName: AttachmentName}
}

func (t codexThread) reopen() error {
	t.agent.thread = ""
	return t.agent.startThread(t.workdir, t.mcpURL, t.localAuthorizing)
}
