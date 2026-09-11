package jobs

import (
	"context"
	"testing"
	"time"
)

// A job cancelled while running stays cancel_requested until its worker acknowledges.
// If that worker is gone (a restart, a hung handler whose process was replaced) nothing
// renews the lease, and the job must still finish rather than hold its concurrency slot
// forever.
func TestOrphanedCancelRequestedJobIsFinishedAndFreesItsSlot(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	if err := f.q.Register("hello", Def{
		Name:        "work",
		Timeout:     5 * time.Second,
		Concurrency: 1,
		Handler:     func(Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orphan, err := f.q.Enqueue(ctx, "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Exec(ctx,
		`UPDATE core_jobs
		    SET state = 'cancel_requested', cancel_reason = ?, attempt = 1,
		        lease_owner = 'gone', lease_expires_at = ?, fence_generation = 1
		  WHERE id = ?`,
		ReasonUser, rfc(f.clock().Add(-time.Minute)), orphan); err != nil {
		t.Fatal(err)
	}
	next, err := f.q.Enqueue(ctx, "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}

	f.start()
	j := f.waitState(orphan, StateCancelled, 2*time.Second)
	if j.CancelReason != ReasonUser {
		t.Errorf("reason = %q, want %q", j.CancelReason, ReasonUser)
	}
	f.waitState(next, StateSucceeded, 2*time.Second)
}
