package harness

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type fakeRunner struct {
	mu           sync.Mutex
	process      *fakeProcess
	command      Command
	stdout       string
	stderr       string
	stdoutChunks [][]byte
}

func (r *fakeRunner) Start(command Command, stdout, stderr io.Writer) (Process, error) {
	r.mu.Lock()
	r.command = command
	p := r.process
	r.mu.Unlock()
	if p == nil {
		return nil, errors.New("no fake process")
	}
	if len(r.stdoutChunks) == 0 {
		_, _ = io.WriteString(stdout, r.stdout)
	} else {
		for _, chunk := range r.stdoutChunks {
			_, _ = stdout.Write(chunk)
		}
	}
	_, _ = io.WriteString(stderr, r.stderr)
	return p, nil
}

type processResult struct {
	code int
	err  error
}

type fakeProcess struct {
	result      chan processResult
	interrupted chan struct{}
	killed      chan struct{}
	interrupt   sync.Once
	kill        sync.Once
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{
		result:      make(chan processResult, 1),
		interrupted: make(chan struct{}),
		killed:      make(chan struct{}),
	}
}

func (p *fakeProcess) Wait() (int, error) {
	result := <-p.result
	return result.code, result.err
}

func (p *fakeProcess) Interrupt() error {
	p.interrupt.Do(func() { close(p.interrupted) })
	return nil
}

func (p *fakeProcess) Kill() error {
	p.kill.Do(func() { close(p.killed) })
	return nil
}

func (p *fakeProcess) exit(code int, err error) { p.result <- processResult{code: code, err: err} }

type fixture struct {
	store   *storage.Store
	bus     *events.Log
	service *Service
	runner  *fakeRunner
	root    string
}

func newFixture(t *testing.T, tweak func(*Options)) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "harness.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bus, err := events.New(store, store, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{process: newFakeProcess()}
	opts := Options{
		Profiles: []Profile{{
			ID: "agent", Name: "Agent", Command: "agent", Args: []string{"run"},
			WorkspaceRoot: root, AcceptsInstruction: true,
		}},
		Runner: runner, StopTimeout: 20 * time.Millisecond, OutputEventGap: 5 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&opts)
	}
	service, err := New(ctx, store, store, bus, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !runner.processDone() {
			runner.process.exit(0, nil)
		}
		_ = service.Close(context.Background())
	})
	return &fixture{store: store, bus: bus, service: service, runner: runner, root: root}
}

func (r *fakeRunner) processDone() bool {
	select {
	case result := <-r.process.result:
		r.process.result <- result
		return true
	default:
		return false
	}
}

func TestCreatePersistsOutputAndExit(t *testing.T) {
	f := newFixture(t, func(opts *Options) { opts.MaxOutputBytes = 4 << 10 })
	f.runner.stdout = strings.Repeat("a", 5000)
	f.runner.stderr = "warning\n"

	session, err := f.service.Create(context.Background(), CreateInput{
		ProfileID: "agent", Title: "Review", Workspace: "project", Instruction: "inspect this",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.State != StateRunning {
		t.Fatalf("state = %q, want running", session.State)
	}
	f.runner.mu.Lock()
	command := f.runner.command
	f.runner.mu.Unlock()
	if command.Dir != filepath.Join(f.root, "project") || strings.Join(command.Args, "|") != "run|inspect this" {
		t.Fatalf("command = %#v", command)
	}

	f.runner.process.exit(0, nil)
	finished := waitState(t, f.service, session.ID, StateExited)
	if finished.ExitCode == nil || *finished.ExitCode != 0 {
		t.Fatalf("exit code = %v", finished.ExitCode)
	}
	var output strings.Builder
	for _, chunk := range finished.Output {
		output.WriteString(chunk.Text)
	}
	if output.Len() > 4<<10 || !strings.Contains(output.String(), "warning") {
		t.Fatalf("retained output length = %d, output = %q", output.Len(), output.String())
	}
	eventRows, err := f.bus.Query(context.Background(), events.Query{Pattern: "core.harness.**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(eventRows) < 3 {
		t.Fatalf("events = %d, want at least created, started, exited", len(eventRows))
	}
}

func TestCreateRejectsWorkspaceEscapeAndCapacity(t *testing.T) {
	f := newFixture(t, func(opts *Options) { opts.MaxSessions = 1 })
	if _, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: ".."}); !errors.Is(err, ErrWorkspaceNotAllowed) {
		t.Fatalf("workspace escape error = %v", err)
	}
	first, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: "project"}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	f.runner.process.exit(0, nil)
	waitState(t, f.service, first.ID, StateExited)
}

func TestStopInterruptsAndRecordsReason(t *testing.T) {
	f := newFixture(t, nil)
	session, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.runner.process.interrupted:
	case <-time.After(time.Second):
		t.Fatal("process was not interrupted")
	}
	f.runner.process.exit(-1, errors.New("interrupted"))
	finished := waitState(t, f.service, session.ID, StateStopped)
	if finished.StopReason != "user" {
		t.Fatalf("stop reason = %q", finished.StopReason)
	}
}

func TestNewRecoversActiveRows(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.store.Exec(context.Background(), `
INSERT INTO core_harness_sessions
    (profile_id, profile_name, title, workspace, state, created_at)
VALUES ('agent', 'Agent', 'Lost', ?, 'running', ?)`, f.root, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), f.store, f.store, f.bus, Options{
		Profiles: []Profile{{ID: "agent", Command: "agent", WorkspaceRoot: f.root}},
		Runner:   f.runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := restarted.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].State != StateInterrupted || sessions[0].FinishedAt == nil {
		t.Fatalf("recovered session = %#v", sessions[0])
	}
}

func waitState(t *testing.T, service *Service, id int64, want State) *Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, err := service.Get(context.Background(), id)
		if err == nil && session.State == want {
			return session
		}
		time.Sleep(5 * time.Millisecond)
	}
	session, err := service.Get(context.Background(), id)
	t.Fatalf("state = %v, err = %v; want %q", session, err, want)
	return nil
}
