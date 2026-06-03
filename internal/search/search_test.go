package search

import (
	"testing"

	"github.com/msradam/waqwaq/internal/store"
)

func TestFTSSearchAndRefresh(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("alpha", "---\ntitle: Alpha\n---\nthe quick brown fox\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("beta", "---\ntitle: Beta\n---\nlazy dog sleeps\n", "", "m"); err != nil {
		t.Fatal(err)
	}

	ix, err := New(st)
	if err != nil {
		t.Skipf("FTS5 unavailable in this build: %v", err)
	}
	defer ix.Close()

	hits, err := ix.Search("quick")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Slug != "alpha" {
		t.Fatalf("search 'quick' = %+v, want one hit on alpha", hits)
	}

	if hits, _ := ix.Search("laz"); len(hits) != 1 || hits[0].Slug != "beta" {
		t.Fatalf("prefix 'laz' = %+v, want one hit on beta", hits)
	}

	// editing a page changes the signature, so the index rebuilds on next query
	if err := st.Write("beta", "---\ntitle: Beta\n---\nlazy dog runs fast\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := ix.Search("runs"); len(hits) != 1 || hits[0].Slug != "beta" {
		t.Fatalf("after edit, search 'runs' = %+v, want one hit on beta", hits)
	}

	if hits, _ := ix.Search("   "); hits != nil {
		t.Fatalf("blank query should return no hits, got %+v", hits)
	}
}
