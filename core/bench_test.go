package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// genWiki writes n synthetic OKF pages under dir/wiki, each with frontmatter,
// tags, and a few wikilinks to nearby pages, and returns a Core over it.
func genWiki(tb testing.TB, n int) Core {
	tb.Helper()
	dir := tb.TempDir()
	types := []string{"BigQuery Table", "Dataset", "Metric", "Playbook", "Reference"}
	for i := 0; i < n; i++ {
		typ := types[i%len(types)]
		// Link to the next 4 pages, wrapping around, to build a connected graph.
		var links string
		for j := 1; j <= 4; j++ {
			links += fmt.Sprintf("See [[page-%d]]. ", (i+j)%n)
		}
		content := fmt.Sprintf(
			"---\ntitle: Page %d\ntype: %s\ntags: [tag-%d, tag-%d]\ndescription: synthetic page %d\ntimestamp: 2026-01-01T00:00:00Z\n---\n# Page %d\n\nThis is the body of synthetic page %d with some searchable words like quokka-%d and lorem ipsum dolor sit amet. %s\n",
			i, typ, i%50, i%13, i, i, i, i, links)
		p := filepath.Join(dir, "wiki", fmt.Sprintf("page-%d.md", i))
		if i == 0 {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				tb.Fatal(err)
			}
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	c, err := New(dir, false)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

const benchN = 5000

// The 100k tier proves massive-wiki performance. The first call parses the whole
// corpus; subsequent calls hit the signature cache and pay only the stat walk.
const massiveN = 100000

func BenchmarkList100k(b *testing.B) {
	c := genWiki(b, massiveN)
	ctx := context.Background()
	if _, err := c.List(ctx, "", "", "", 0, 0); err != nil { // warm the cache
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.List(ctx, "", "", "", 50, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGraph100k(b *testing.B) {
	c := genWiki(b, massiveN)
	ctx := context.Background()
	if _, err := c.Graph(ctx); err != nil { // warm
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Graph(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRead100k(b *testing.B) {
	c := genWiki(b, massiveN)
	ctx := context.Background()
	if _, err := c.Read(ctx, "page-50000"); err != nil { // warm
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Read(ctx, "page-50000"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkColdParse100k measures first-call latency on a cold cache: the whole
// corpus is parsed once. Keep -benchtime low (e.g. 2x); each iteration writes
// 100k files.
func BenchmarkColdParse100k(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c := genWiki(b, massiveN) // fresh Core: cold cache each iteration
		b.StartTimer()
		if _, err := c.List(ctx, "", "", "", 50, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchLiteral(b *testing.B) {
	c := genWiki(b, benchN)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Search(ctx, "quokka-4242", false, "", "", 20); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchRegex(b *testing.B) {
	c := genWiki(b, benchN)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Search(ctx, `quokka-4\d{3}`, true, "", "", 20); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListAll(b *testing.B) {
	c := genWiki(b, benchN)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.List(ctx, "", "", "", 0, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListByType(b *testing.B) {
	c := genWiki(b, benchN)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.List(ctx, "", "Metric", "", 0, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGraph(b *testing.B) {
	c := genWiki(b, benchN)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Graph(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNeighbors(b *testing.B) {
	c := genWiki(b, benchN)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Neighbors(ctx, "page-2500", 1); err != nil {
			b.Fatal(err)
		}
	}
}
