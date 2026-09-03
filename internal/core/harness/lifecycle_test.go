package harness

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type blockingRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Start(Command, io.Writer, io.Writer) (Process, error) {
	close(r.entered)
	<-r.release
	return immediateProcess{}, nil
}

type immediateProcess struct{}

func (immediateProcess) Wait() (int, error) { return 0, nil }
func (immediateProcess) Interrupt() error   { return nil }
func (immediateProcess) Kill() error        { return nil }

func TestCloseWaitsForAdmittedStart(t *testing.T) {
	runner := &blockingRunner{entered: make(chan struct{}), release: make(chan struct{})}
	f := newFixture(t, func(opts *Options) { opts.Runner = runner })
	createDone := make(chan error, 1)
	go func() {
		_, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: "project"})
		createDone <- err
	}()
	<-runner.entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- f.service.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		close(runner.release)
		<-createDone
		t.Fatalf("Close returned before the admitted Start completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(runner.release)
	if err := <-createDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestOutputPreservesUTF8AcrossRetentionChunks(t *testing.T) {
	f := newFixture(t, func(opts *Options) { opts.MaxOutputBytes = 64 << 10 })
	want := strings.Repeat("a", (32<<10)-1) + "€"
	f.runner.stdoutChunks = [][]byte{
		append([]byte(strings.Repeat("a", (32<<10)-1)), 0xe2),
		{0x82, 0xac},
	}
	session, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: "project"})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.process.exit(0, nil)
	finished := waitState(t, f.service, session.ID, StateExited)
	var got strings.Builder
	for _, chunk := range finished.Output {
		got.WriteString(chunk.Text)
	}
	if got.String() != want {
		t.Fatalf("output suffix = %q, want %q", got.String()[got.Len()-8:], want[len(want)-8:])
	}
}

type finishBlockingDB struct {
	storage.DB
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (db *finishBlockingDB) Tx(ctx context.Context, fn func(storage.Tx) error) error {
	return db.DB.Tx(ctx, func(tx storage.Tx) error {
		return fn(&finishBlockingTx{Tx: tx, db: db})
	})
}

type finishBlockingTx struct {
	storage.Tx
	db *finishBlockingDB
}

func (tx *finishBlockingTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "SET state=?, exit_code=?") {
		tx.db.once.Do(func() { close(tx.db.entered) })
		select {
		case <-tx.db.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.Tx.Exec(ctx, query, args...)
}

func TestStopWaitsForExitPersistence(t *testing.T) {
	f := newFixture(t, nil)
	session, err := f.service.Create(context.Background(), CreateInput{ProfileID: "agent", Workspace: "project"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := &finishBlockingDB{DB: f.service.db, entered: make(chan struct{}), release: make(chan struct{})}
	f.service.db = blocked
	f.runner.process.exit(0, nil)
	<-blocked.entered

	stopDone := make(chan error, 1)
	go func() { stopDone <- f.service.Stop(context.Background(), session.ID) }()
	select {
	case err := <-stopDone:
		close(blocked.release)
		t.Fatalf("Stop returned before exit was persisted: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(blocked.release)
	if err := <-stopDone; !errors.Is(err, ErrTerminal) {
		t.Fatalf("Stop error = %v, want ErrTerminal", err)
	}
}
