package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/tap-runtime/discover"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

type sessionsReader []discover.Session

func (sessionsReader) Client() string                               { return "fake" }
func (r sessionsReader) Read(time.Time) ([]discover.Session, error) { return r, nil }

// ticketReport: in 20 sessions over 6 weeks the user asks to move a ticket
// to done, and the agent transitions it and comments on it.
func ticketReport(t *testing.T) *discover.Report {
	t.Helper()
	var ss sessionsReader
	t0 := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		ticket := fmt.Sprintf("TENG-%d", 3200+i)
		s := discover.Session{Client: "fake", ID: fmt.Sprintf("s%02d", i), Start: t0.AddDate(0, 0, 2*i),
			Requests: []string{"move " + ticket + " to done"}}
		// Sessions must differ: identical step sequences count once.
		noise := []string{"ls", "pwd", "date", "uptime", "hostname"}
		for _, c := range []discover.Call{
			{Tool: "shell", Command: noise[i%len(noise)]},
			{Tool: "shell", Command: noise[(i/len(noise))%len(noise)] + " -a"},
			{Tool: "mcp:telara_jira_transition_issue", Args: map[string]string{"issue_key": ticket, "transition_id": "21"}},
			{Tool: "mcp:telara_jira_add_comment", Args: map[string]string{"issue_key": ticket, "body": "done"}},
		} {
			c.Time = s.Start
			s.Calls = append(s.Calls, c)
		}
		ss = append(ss, s)
	}
	o := discover.DefaultOptions()
	o.Readers = []discover.Reader{ss}
	rep, err := discover.Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Primitives()) == 0 {
		t.Fatalf("no primitive found: %+v", rep.Funnel)
	}
	return rep
}

func TestReviewPublishesUnderTheChosenPublisher(t *testing.T) {
	rep := ticketReport(t)
	var saved, published *discover.Draft
	var audience string
	act := discover.ReviewActions{
		Save:       func(d *discover.Draft) (string, error) { saved = d; return "/skills/" + d.Name, nil },
		CanPublish: func() string { return "" },
		Publish: func(d *discover.Draft, aud string) (string, bool, error) {
			published, audience = d, aud
			return "accepted", true, nil
		},
	}
	var out bytes.Buffer
	// Save 1, publish 1 (asked for a publisher, then the audience).
	if err := discover.Review(strings.NewReader("1\n1\ncom.acme\nt\n"), &out, rep, discover.ReviewConfig{Top: 1}, act); err != nil {
		t.Fatal(err)
	}
	if saved == nil || published == nil || published.Publisher != "com.acme" || audience != "tenant" {
		t.Fatalf("saved %v published %+v to %q\n%s", saved != nil, published, audience, out.String())
	}
	if !strings.Contains(string(published.Files["primitive.yaml"]), "publisher: com.acme") {
		t.Fatalf("the published manifest must carry the chosen publisher:\n%s", published.Files["primitive.yaml"])
	}
}

func TestReviewWithoutSignInPublishesNothing(t *testing.T) {
	rep := ticketReport(t)
	called := false
	act := discover.ReviewActions{
		Save:       func(*discover.Draft) (string, error) { return "", nil },
		CanPublish: func() string { return "not signed in" },
		Publish:    func(*discover.Draft, string) (string, bool, error) { called = true; return "", true, nil },
	}
	var out bytes.Buffer
	if err := discover.Review(strings.NewReader("\n1\n"), &out, rep, discover.ReviewConfig{Publisher: "com.acme"}, act); err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(out.String(), "Publishing is unavailable: not signed in") {
		t.Fatalf("called=%v\n%s", called, out.String())
	}
}

func TestSavedDraftInstallsThroughTheTelaraInstaller(t *testing.T) {
	rep := ticketReport(t)
	d := rep.Primitives()[0].Draft()
	pkg, digest, err := d.Package()
	if err != nil {
		t.Fatal(err)
	}
	res, err := skillshare.InstallPrimitive(t.TempDir(), skillshare.PrimitiveInstall{Publisher: d.Publisher, Name: d.Name, Version: "0.1.0", ArtifactDigest: digest, Package: pkg}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"primitive.yaml", "main.sh", "SKILL.md"} {
		if _, err := os.Stat(filepath.Join(res.Path, f)); err != nil {
			t.Errorf("installed folder lacks %s: %v", f, err)
		}
	}
}

func TestPublishGoesThroughTelaraSkillPublish(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		var msg struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &msg)
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-06-18"}}`, msg.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Errorf("session header not carried")
			}
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name != "telara_skill_publish" {
				t.Errorf("tool = %s", p.Name)
			}
			got = p.Arguments
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"accepted com.acme/x@0.1.0\"}]}}\n\n", msg.ID)
		}
	}))
	defer srv.Close()
	m := &mcpCaller{endpoint: srv.URL, key: "k"}
	ctx := context.Background()
	if err := m.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	text, isErr, err := m.callTool(ctx, "telara_skill_publish", publishArgs("x", "desc", "com.acme", "user", "u-1", map[string][]byte{"primitive.yaml": []byte("kind: Primitive"), "main.sh": []byte("ls")}))
	if err != nil || isErr || text != "accepted com.acme/x@0.1.0" {
		t.Fatalf("text %q isErr %v err %v", text, isErr, err)
	}
	files, _ := got["files"].(map[string]any)
	if got["publisher"] != "com.acme" || got["target_scope_type"] != "user" || got["target_scope_id"] != "u-1" || files["main.sh"] != "ls" {
		t.Fatalf("arguments = %v", got)
	}
}
