//go:build integration

package ledger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestConcurrentRuns: two runners of the same migration both pass Start;
// the second waits on Claim's row lock, then refuses (the first applied it).
func TestConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	url := testpg.Start(t)
	c1, err := executor.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close(ctx)
	c2, err := executor.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close(ctx)

	l := Ledger{Schema: "dpg", Version: "test"}
	m := Migration{Log: "app", ID: "m1", Parent: "", Checksum: "c1"}
	for _, s := range append(l.Bootstrap(), l.Start(m)) {
		if _, err := c1.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// The second runner's Start takes over the started row.
	if _, err := c2.Exec(ctx, l.Start(m)); err != nil {
		t.Fatalf("second start: %v", err)
	}

	tx1, err := c1.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx1.Exec(ctx, l.Claim(m)); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		tx2, err := c2.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		_, err = tx2.Exec(ctx, l.Claim(m))
		tx2.Rollback(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the second claim didn't wait: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := tx1.Exec(ctx, l.Finish(m, Complete)); err != nil {
		t.Fatal(err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not waiting to run") {
			t.Errorf("second claim after the first committed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second claim never returned")
	}

	// The next migration must follow it; one that doesn't is refused.
	stray := Migration{Log: "app", ID: "m3", Parent: "m2", Checksum: "c3"}
	if _, err := c1.Exec(ctx, l.Start(stray)); err == nil || !strings.Contains(err.Error(), "DPG-E050") {
		t.Errorf("out of order: %v", err)
	}
	// Recorded with other statements: refused.
	if _, err := c1.Exec(ctx, l.Start(Migration{Log: "app", ID: "m1", Checksum: "other"})); err == nil || !strings.Contains(err.Error(), "different statements") {
		t.Errorf("other checksum: %v", err)
	}
	// Another log in the same table is independent.
	if _, err := c1.Exec(ctx, l.Start(Migration{Log: "_cluster", ID: "r1", Checksum: "x"})); err != nil {
		t.Errorf("another log: %v", err)
	}
	st, err := l.Read(ctx, c1, "app")
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := st.Head(); h != "m1" || len(st.Rows) != 1 {
		t.Errorf("read: head %q, rows %v", h, st.Rows)
	}
}
