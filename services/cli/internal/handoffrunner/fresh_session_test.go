package handoffrunner

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// freshSessionRefusal is the response agent-service gives for a thread that
// already carries earlier input outside the work's envelope.
func freshSessionRefusal() error {
	msg := "this provider thread already carries earlier input outside this work's sensitivity envelope; start a fresh session of the same agent and ask again"
	return &api.APIError{StatusCode: http.StatusBadRequest, Message: msg, Body: `{"code":9,"message":"` + msg + `"}`}
}

type inputControl struct {
	Control
	answers []error
	seen    []api.HandoffRunAttachment
}

func (c *inputControl) GetHandoffRunInput(_ context.Context, _, _ string, seen api.HandoffRunAttachment) (*api.HandoffRunInput, error) {
	c.seen = append(c.seen, seen)
	err := c.answers[0]
	c.answers = c.answers[1:]
	if err != nil {
		return nil, err
	}
	return &api.HandoffRunInput{BriefJSON: "{}"}, nil
}

type stubThread struct {
	id      string
	reopens int
	failing bool
}

func (s *stubThread) attachment() api.HandoffRunAttachment {
	return api.HandoffRunAttachment{RuntimeVersion: "1", ThreadID: s.id, AttachmentName: AttachmentName}
}

func (s *stubThread) reopen() error {
	s.reopens++
	if s.failing {
		return errors.New("attachment failed")
	}
	s.id = s.id + "-fresh"
	return nil
}

func TestFreshSessionRefusalOpensOneNewThreadAndAsksOnce(t *testing.T) {
	control := &inputControl{answers: []error{freshSessionRefusal(), nil}}
	thread := &stubThread{id: "t1"}
	input, err := takeFirstInput(context.Background(), control, "exec", "inc", thread, nil)
	if err != nil || input == nil {
		t.Fatalf("takeFirstInput = %v, %v", input, err)
	}
	if thread.reopens != 1 || len(control.seen) != 2 || control.seen[0].ThreadID != "t1" || control.seen[1].ThreadID != "t1-fresh" {
		t.Fatalf("reopens=%d seen=%+v", thread.reopens, control.seen)
	}
}

// A second refusal ends the run; the runner never loops.
func TestFreshSessionRefusedTwiceEndsTheRun(t *testing.T) {
	control := &inputControl{answers: []error{freshSessionRefusal(), freshSessionRefusal()}}
	thread := &stubThread{id: "t1"}
	if _, err := takeFirstInput(context.Background(), control, "exec", "inc", thread, nil); !FreshSessionRequired(err) {
		t.Fatalf("second refusal must be returned, got %v", err)
	}
	if thread.reopens != 1 || len(control.seen) != 2 {
		t.Fatalf("asked %d times, reopened %d", len(control.seen), thread.reopens)
	}
}

// Every other refusal is returned as it was, with no new thread.
func TestOtherRefusalsAreNotRetried(t *testing.T) {
	for _, refusal := range []error{
		&api.APIError{StatusCode: http.StatusBadRequest, Message: "this run already started, so it can only be reported on or reconciled"},
		&api.APIError{StatusCode: http.StatusForbidden, Message: "organization policy does not allow this run's input to leave"},
		errors.New("network down"),
	} {
		control := &inputControl{answers: []error{refusal}}
		thread := &stubThread{id: "t1"}
		if _, err := takeFirstInput(context.Background(), control, "exec", "inc", thread, nil); !errors.Is(err, refusal) {
			t.Fatalf("got %v, want %v", err, refusal)
		}
		if thread.reopens != 0 || len(control.seen) != 1 {
			t.Fatalf("%v: asked %d times, reopened %d", refusal, len(control.seen), thread.reopens)
		}
	}
}

func TestFreshThreadThatCannotAttachSendsNothing(t *testing.T) {
	control := &inputControl{answers: []error{freshSessionRefusal()}}
	thread := &stubThread{id: "t1", failing: true}
	if _, err := takeFirstInput(context.Background(), control, "exec", "inc", thread, nil); err == nil {
		t.Fatal("a fresh thread that cannot attach must end the run")
	}
	if len(control.seen) != 1 {
		t.Fatalf("asked %d times", len(control.seen))
	}
}

// The real Codex thread (the fake app-server): reopening starts a new thread
// on the same agent process, attached to the same grant session only, before
// anything is sent.
func TestCodexThreadReopensANewThreadOfTheSameAgent(t *testing.T) {
	up := newUpstream(http.StatusOK)
	defer up.Close()
	proxy, err := startSessionProxy(up.URL+"/v1/mcp", "telara_handoff_t.i.s", testKey(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	home := privateHome(t, true)
	work := t.TempDir()
	c, err := startCodex(fakeCodexBin, home, work)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := c.startThread(work, proxy.URL(), proxy.localAuthorization()); err != nil {
		t.Fatal(err)
	}
	thread := codexThread{agent: c, workdir: work, mcpURL: proxy.URL(), localAuthorizing: proxy.localAuthorization()}
	first := thread.attachment()
	if err := thread.reopen(); err != nil {
		t.Fatal(err)
	}
	second := thread.attachment()
	if second.ThreadID == "" || second.ThreadID == first.ThreadID || second.RuntimeVersion != first.RuntimeVersion {
		t.Fatalf("first %+v second %+v", first, second)
	}
	starts := 0
	for _, r := range records(t, home) {
		switch r["kind"] {
		case "thread_start":
			starts++
		case "turn_start":
			t.Fatal("input sent while reopening")
		}
	}
	if starts != 2 {
		t.Fatalf("thread starts = %d, want 2", starts)
	}
}
