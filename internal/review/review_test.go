package review

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/msradam/waqwaq/internal/store"
)

func TestDeleteProposalMerge(t *testing.T) {
	q, st := newQueue(t)
	if err := st.Write("doomed", "---\ntitle: Doomed\n---\nbye\n", "", "add"); err != nil {
		t.Fatal(err)
	}
	p, err := q.CreateDelete("doomed", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsDelete() {
		t.Fatalf("proposal Op = %q, want delete", p.Op)
	}
	if _, err := st.Read("doomed"); err != nil {
		t.Fatalf("page should still exist before merge: %v", err)
	}
	if _, err := q.Merge(p.ID, "boss"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, err := st.Read("doomed"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after merging the delete proposal, page still readable: %v", err)
	}
	if _, err := q.CreateDelete("nope", "agent"); err == nil {
		t.Error("CreateDelete on a missing page should error")
	}
}

func newQueue(t *testing.T) (*Queue, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(st, "")
	if err != nil {
		t.Fatal(err)
	}
	return q, st
}

func TestWebhookFiresOnCreate(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
	}))
	defer srv.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(st, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Create("foo", "---\ntitle: Foo\n---\n", "ci-bot", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-got:
		if body["slug"] != "foo" || body["event"] != "proposal.created" || body["author"] != "ci-bot" {
			t.Fatalf("unexpected webhook payload: %+v", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook was not received")
	}
}

func TestCreateThenMergeWritesPage(t *testing.T) {
	q, st := newQueue(t)
	p, err := q.Create("foo", "---\ntitle: Foo\n---\nbody\n", "ci-bot", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != Pending || p.Author != "ci-bot" || p.Title != "Foo" {
		t.Fatalf("unexpected proposal %+v", p)
	}
	if _, err := st.Read("foo"); err == nil {
		t.Fatal("page should not exist before merge")
	}

	merged, err := q.Merge(p.ID, "adam")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Status != Merged || merged.Reviewer != "adam" {
		t.Fatalf("unexpected merged proposal %+v", merged)
	}
	page, err := st.Read("foo")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Foo" {
		t.Errorf("merged page title = %q", page.Title)
	}
	if _, err := q.Merge(p.ID, "adam"); err == nil {
		t.Error("merging an already-merged proposal should fail")
	}
}

func TestReject(t *testing.T) {
	q, _ := newQueue(t)
	p, _ := q.Create("bar", "---\ntitle: Bar\n---\n", "ci-bot", nil)
	r, err := q.Reject(p.ID, "adam", "not now")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Rejected || r.Reason != "not now" {
		t.Fatalf("unexpected rejected proposal %+v", r)
	}
}

func TestStaleWhenBaseChanges(t *testing.T) {
	q, st := newQueue(t)
	if err := st.Write("baz", "---\ntitle: Base\n---\nv1\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	p, _ := q.Create("baz", "---\ntitle: Edit\n---\nv2\n", "ci-bot", nil)
	if q.Stale(p) {
		t.Fatal("a fresh proposal should not be stale")
	}
	if err := st.Write("baz", "---\ntitle: Base\n---\nv1-changed\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	if !q.Stale(p) {
		t.Fatal("proposal should be stale after its base page changed")
	}
}

func TestPendingCount(t *testing.T) {
	q, _ := newQueue(t)
	if _, err := q.Create("a", "---\ntitle: A\n---\n", "x", nil); err != nil {
		t.Fatal(err)
	}
	p, _ := q.Create("b", "---\ntitle: B\n---\n", "x", nil)
	if _, err := q.Merge(p.ID, "adam"); err != nil {
		t.Fatal(err)
	}
	n, err := q.PendingCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pending count = %d, want 1", n)
	}
}
