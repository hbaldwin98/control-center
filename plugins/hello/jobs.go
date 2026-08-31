package hello

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const (
	minuteTimeout = time.Minute
	blobKey       = "latest.txt"
)

type tickArgs struct {
	// Hold waits until the job context is cancelled. Tests use it to disable
	// a running tick; cron never sets it.
	Hold bool `json:"hold"`
}

type ticked struct {
	At      string `json:"at"`
	Note    string `json:"note"`
	BlobKey string `json:"blobKey"`
	AIText  string `json:"aiText"`
}

func (p *Plugin) tick(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("hello: host is not initialized")
	}

	var args tickArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	if args.Hold {
		_ = jc.Logf("holding until cancelled")
		<-jc.Done()
		return jc.Err()
	}

	if err := jc.Err(); err != nil {
		return err
	}
	if err := jc.Progress(0.1, "starting"); err != nil {
		return err
	}

	now := h.Clock().Now().UTC()
	note := p.settings().Note
	h.Log().Info("tick", "at", now, "note", note)
	_ = jc.Logf("tick at %s note %s", now.Format(time.RFC3339), note)

	aiText := ""
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:    "cheap-chat",
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: "ping"}},
	})
	if err != nil {
		_ = jc.Logf("ai call failed: %v", err)
	} else {
		aiText = resp.Text
	}

	if err := jc.Err(); err != nil {
		return err
	}

	if err := p.fetchHello(jc, h); err != nil {
		_ = jc.Logf("browser call failed: %v", err)
	}

	payload := []byte(note + " " + now.Format(time.RFC3339Nano))
	if _, err := h.Blobs().Put(jc, blobKey, bytes.NewReader(payload), "text/plain"); err != nil {
		return err
	}
	rc, _, err := h.Blobs().Get(jc, blobKey)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()

	if err := jc.Progress(0.9, "publishing"); err != nil {
		return err
	}
	return h.Events().Publish(jc, "ticked", now.Format(time.RFC3339Nano), ticked{
		At:      now.Format(time.RFC3339Nano),
		Note:    note,
		BlobKey: blobKey,
		AIText:  aiText,
	})
}

func (p *Plugin) onTicked(ctx context.Context, tx hoststorage.Tx, e hostevents.Event) error {
	var body ticked
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &body)
	}
	if body.At == "" {
		body.At = e.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO hello_ticks(at, note, blob_key, ai_text, event_id) VALUES (?, ?, ?, ?, ?)`,
		body.At, body.Note, body.BlobKey, body.AIText, e.ID)
	return err
}

func (p *Plugin) fetchHello(jc hostjobs.Context, h host.Host) error {
	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		return err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return err
	}
	defer page.Close(jc)
	if err := page.Goto(jc, "https://hello.test/"); err != nil {
		return err
	}
	if err := page.WaitFor(jc, "article.lot", 5*time.Second); err != nil {
		return err
	}
	_, err = page.Content(jc)
	return err
}
