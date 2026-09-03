// Package harness runs administrator-configured coding-agent commands and keeps a
// durable, bounded record of their lifecycle and output.
//
// Layer 3. It imports storage and events only.
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type State string

const (
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateStopping    State = "stopping"
	StateExited      State = "exited"
	StateFailed      State = "failed"
	StateStopped     State = "stopped"
	StateInterrupted State = "interrupted"
)

func (s State) Terminal() bool {
	switch s {
	case StateExited, StateFailed, StateStopped, StateInterrupted:
		return true
	default:
		return false
	}
}

// Profile is one deployment-approved command and workspace boundary.
type Profile struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Command            string   `json:"-"`
	Args               []string `json:"-"`
	WorkspaceRoot      string   `json:"workspaceRoot"`
	AcceptsInstruction bool     `json:"acceptsInstruction"`
}

type CreateInput struct {
	ProfileID   string
	Title       string
	Workspace   string
	Instruction string
}

type Session struct {
	ID              int64         `json:"id"`
	ProfileID       string        `json:"profileId"`
	ProfileName     string        `json:"profileName"`
	Title           string        `json:"title"`
	Workspace       string        `json:"workspace"`
	State           State         `json:"state"`
	ExitCode        *int          `json:"exitCode"`
	Error           string        `json:"error"`
	StopReason      string        `json:"stopReason"`
	CreatedAt       time.Time     `json:"createdAt"`
	StartedAt       *time.Time    `json:"startedAt"`
	FinishedAt      *time.Time    `json:"finishedAt"`
	Output          []OutputChunk `json:"output,omitempty"`
	OutputTruncated bool          `json:"outputTruncated,omitempty"`
}

type OutputChunk struct {
	ID        int64     `json:"id"`
	Stream    string    `json:"stream"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

type Options struct {
	Profiles       []Profile
	Runner         Runner
	MaxSessions    int
	MaxOutputBytes int64
	StopTimeout    time.Duration
	OutputEventGap time.Duration
	Now            func() time.Time
}

func (o *Options) defaults() {
	if o.Runner == nil {
		o.Runner = nativeRunner{}
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = 4
	}
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = 1 << 20
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 5 * time.Second
	}
	if o.OutputEventGap <= 0 {
		o.OutputEventGap = 500 * time.Millisecond
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

type Service struct {
	db       storage.DB
	bus      events.Bus
	opts     Options
	profiles map[string]Profile

	mu       sync.Mutex
	running  map[int64]*runningSession
	starting int
	closed   bool
	wg       sync.WaitGroup
}

type runningSession struct {
	process    Process
	stopReason string
	exited     bool
	dirty      atomic.Bool
	done       chan struct{}
	stdout     *outputWriter
	stderr     *outputWriter
}

var (
	ErrUnknownProfile      = errors.New("harness: unknown profile")
	ErrInvalidInput        = errors.New("harness: invalid input")
	ErrWorkspaceNotAllowed = errors.New("harness: workspace is outside the profile root")
	ErrCapacity            = errors.New("harness: session capacity reached")
	ErrNotFound            = errors.New("harness: session not found")
	ErrTerminal            = errors.New("harness: session is already terminal")
	ErrUnavailable         = errors.New("harness: service is stopping")
)

func New(ctx context.Context, m storage.Migrator, db storage.DB, bus events.Bus, opts Options) (*Service, error) {
	opts.defaults()
	profiles, err := normalizeProfiles(opts.Profiles)
	if err != nil {
		return nil, err
	}
	if err := m.Apply("harness", migrations); err != nil {
		return nil, err
	}
	s := &Service{db: db, bus: bus, opts: opts, profiles: profiles, running: make(map[int64]*runningSession)}
	if err := s.recoverInterrupted(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func normalizeProfiles(in []Profile) (map[string]Profile, error) {
	out := make(map[string]Profile, len(in))
	for _, p := range in {
		p.ID = strings.TrimSpace(p.ID)
		p.Name = strings.TrimSpace(p.Name)
		p.Command = strings.TrimSpace(p.Command)
		if p.Name == "" {
			p.Name = p.ID
		}
		if !validID(p.ID) || p.Command == "" || strings.TrimSpace(p.WorkspaceRoot) == "" {
			return nil, fmt.Errorf("harness: invalid profile %q", p.ID)
		}
		if _, exists := out[p.ID]; exists {
			return nil, fmt.Errorf("harness: duplicate profile %q", p.ID)
		}
		root, err := filepath.Abs(p.WorkspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("harness: profile %q workspace root: %w", p.ID, err)
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("harness: profile %q workspace root: %w", p.ID, err)
		}
		p.WorkspaceRoot = filepath.Clean(root)
		p.Args = append([]string(nil), p.Args...)
		out[p.ID] = p
	}
	return out, nil
}

func validID(s string) bool {
	if len(s) < 1 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (s *Service) Profiles() []Profile {
	out := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Session, error) {
	p, ok := s.profiles[strings.TrimSpace(in.ProfileID)]
	if !ok {
		return nil, ErrUnknownProfile
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Instruction = strings.TrimSpace(in.Instruction)
	if in.Title == "" {
		in.Title = p.Name
	}
	if len(in.Title) > 120 || len(in.Instruction) > 32<<10 || (in.Instruction != "" && !p.AcceptsInstruction) {
		return nil, ErrInvalidInput
	}
	workspace, err := resolveWorkspace(p.WorkspaceRoot, in.Workspace)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	if len(s.running)+s.starting >= s.opts.MaxSessions {
		s.mu.Unlock()
		return nil, ErrCapacity
	}
	s.starting++
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.starting--
		s.mu.Unlock()
		s.wg.Done()
	}()

	created := s.opts.Now().UTC()
	id, err := s.insertStarting(ctx, p, in.Title, workspace, created)
	if err != nil {
		return nil, err
	}
	run := &runningSession{done: make(chan struct{})}
	args := append([]string(nil), p.Args...)
	if in.Instruction != "" {
		args = append(args, in.Instruction)
	}
	stdout := &outputWriter{service: s, sessionID: id, stream: "stdout", dirty: &run.dirty}
	stderr := &outputWriter{service: s, sessionID: id, stream: "stderr", dirty: &run.dirty}
	run.stdout = stdout
	run.stderr = stderr
	proc, err := s.opts.Runner.Start(Command{Path: p.Command, Args: args, Dir: workspace}, stdout, stderr)
	if err != nil {
		run.flushOutput()
		_ = s.finish(ctx, id, StateFailed, nil, err.Error(), "")
		return nil, fmt.Errorf("harness: start profile %q: %w", p.ID, err)
	}
	run.process = proc
	started := s.opts.Now().UTC()
	if err := s.markStarted(ctx, id, started); err != nil {
		_ = proc.Kill()
		_, _ = proc.Wait()
		run.flushOutput()
		_ = s.finish(context.Background(), id, StateFailed, nil, err.Error(), "")
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = proc.Kill()
		_, _ = proc.Wait()
		run.flushOutput()
		_ = s.finish(context.Background(), id, StateInterrupted, nil, "service stopped during launch", "shutdown")
		return nil, ErrUnavailable
	}
	s.running[id] = run
	s.wg.Add(2)
	s.mu.Unlock()
	go s.wait(id, run)
	go s.outputEvents(id, run)
	return s.Get(ctx, id)
}

func resolveWorkspace(root, relative string) (string, error) {
	relative = strings.TrimSpace(relative)
	if filepath.IsAbs(relative) {
		return "", ErrWorkspaceNotAllowed
	}
	candidate := filepath.Join(root, filepath.Clean(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrWorkspaceNotAllowed
	}
	return resolved, nil
}

func (s *Service) wait(id int64, run *runningSession) {
	defer s.wg.Done()
	code, waitErr := run.process.Wait()
	s.mu.Lock()
	run.exited = true
	reason := run.stopReason
	s.mu.Unlock()
	run.flushOutput()
	state := StateExited
	errText := ""
	var exitCode *int
	if code >= 0 {
		exitCode = &code
	}
	if reason != "" {
		state = StateStopped
	} else if waitErr != nil || code != 0 {
		state = StateFailed
		if waitErr != nil {
			errText = waitErr.Error()
		}
	}
	if err := s.finish(context.Background(), id, state, exitCode, errText, reason); err != nil {
		slog.Error("harness: persist process exit", "session", id, "err", err)
	}
	close(run.done)
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func (run *runningSession) flushOutput() {
	run.stdout.flush()
	run.stderr.flush()
}

func (s *Service) outputEvents(id int64, run *runningSession) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.opts.OutputEventGap)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.publishOutput(id, run)
		case <-run.done:
			s.publishOutput(id, run)
			return
		}
	}
}

func (s *Service) publishOutput(id int64, run *runningSession) {
	if s.bus == nil || !run.dirty.Swap(false) {
		return
	}
	if _, err := s.bus.Publish(context.Background(), events.Input{
		Type: events.TypeHarnessOutput, Source: events.SourceHarness, Subject: fmt.Sprint(id),
		Payload: map[string]any{"sessionId": id},
	}); err != nil {
		slog.Debug("harness: publish output invalidation", "session", id, "err", err)
	}
}

func (s *Service) Stop(ctx context.Context, id int64) error {
	return s.stop(ctx, id, "user")
}

func (s *Service) stop(ctx context.Context, id int64, reason string) error {
	s.mu.Lock()
	run := s.running[id]
	if run == nil {
		s.mu.Unlock()
		return s.stoppedSessionError(ctx, id)
	}
	if run.exited {
		done := run.done
		s.mu.Unlock()
		select {
		case <-done:
			return s.stoppedSessionError(ctx, id)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if run.stopReason != "" {
		s.mu.Unlock()
		return nil
	}
	run.stopReason = reason
	if err := s.markStopping(ctx, id, reason); err != nil {
		run.stopReason = ""
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	if err := run.process.Interrupt(); err != nil {
		_ = run.process.Kill()
		return nil
	}
	go func() {
		timer := time.NewTimer(s.opts.StopTimeout)
		defer timer.Stop()
		select {
		case <-run.done:
		case <-timer.C:
			_ = run.process.Kill()
		}
	}()
	return nil
}

func (s *Service) stoppedSessionError(ctx context.Context, id int64) error {
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if session.State.Terminal() {
		return ErrTerminal
	}
	return ErrNotFound
}

// Close stops every live process and waits for process and output goroutines.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	ids := make([]int64, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	var stopErr error
	for _, id := range ids {
		if err := s.stop(ctx, id, "shutdown"); err != nil && !errors.Is(err, ErrTerminal) {
			stopErr = errors.Join(stopErr, err)
		}
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type outputWriter struct {
	service   *Service
	sessionID int64
	stream    string
	dirty     *atomic.Bool
	mu        sync.Mutex
	pending   []byte
}

func (w *outputWriter) Write(p []byte) (int, error) {
	total := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()

	data := make([]byte, 0, len(w.pending)+len(p))
	data = append(data, w.pending...)
	data = append(data, p...)
	complete := completeUTF8Prefix(data)
	w.pending = append(w.pending[:0], data[complete:]...)
	w.persist(strings.ToValidUTF8(string(data[:complete]), "?"))
	return total, nil
}

func completeUTF8Prefix(p []byte) int {
	for i := 0; i < len(p); {
		if !utf8.FullRune(p[i:]) {
			return i
		}
		_, size := utf8.DecodeRune(p[i:])
		i += size
	}
	return len(p)
}

func (w *outputWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return
	}
	w.persist(strings.ToValidUTF8(string(w.pending), "?"))
	w.pending = nil
}

func (w *outputWriter) persist(text string) {
	chunkSize := 32 << 10
	if w.service.opts.MaxOutputBytes < int64(chunkSize) {
		chunkSize = int(w.service.opts.MaxOutputBytes)
	}
	for len(text) > 0 {
		n := min(len(text), chunkSize)
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		if n == 0 {
			_, n = utf8.DecodeRuneInString(text)
		}
		if err := w.service.appendOutput(context.Background(), w.sessionID, w.stream, text[:n]); err != nil {
			slog.Debug("harness: persist output", "session", w.sessionID, "err", err)
		} else {
			w.dirty.Store(true)
		}
		text = text[n:]
	}
}

var _ io.Writer = (*outputWriter)(nil)

func (s *Service) recoverInterrupted(ctx context.Context) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM core_harness_sessions WHERE state IN ('starting','running','stopping')`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		at := s.opts.Now().UTC().Format(time.RFC3339Nano)
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE core_harness_sessions SET state='interrupted', error='control center restarted', finished_at=? WHERE id=?`, at, id); err != nil {
				return err
			}
			if err := s.publishTx(ctx, tx, events.TypeHarnessInterrupted, id, map[string]any{"sessionId": id}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) publishTx(ctx context.Context, tx storage.Tx, typ string, id int64, payload any) error {
	if s.bus == nil {
		return nil
	}
	_, err := s.bus.PublishTx(ctx, tx, events.Input{Type: typ, Source: events.SourceHarness, Subject: fmt.Sprint(id), Payload: payload})
	return err
}
