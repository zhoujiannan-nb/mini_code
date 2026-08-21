package web

import "testing"

func newTestIndex() *DirIndex {
	return &DirIndex{
		first: make(chan struct{}),
		dirs: []string{
			`D:\`,
			`D:\project`,
			`D:\project\car_home`,
			`D:\project\car_home\src`,
			`D:\project\other`,
			`C:\`,
			`C:\Users\flycat666`,
			`C:\Users\flycat666\Documents\car_home_backup`,
		},
		roots: map[string]struct{}{`D:\`: {}, `C:\`: {}},
		ready: true,
	}
}

func TestDirIndexSearchRanking(t *testing.T) {
	ix := newTestIndex()

	hits, indexing := ix.Search("car_home", 20)
	if indexing {
		t.Fatal("index should be ready")
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for car_home")
	}
	// Exact base-name match must rank first.
	if hits[0].Path != `D:\project\car_home` {
		t.Fatalf("first hit = %q, want D:\\project\\car_home", hits[0].Path)
	}
	// Prefix-in-base match (car_home_backup) must also be found.
	var found bool
	for _, h := range hits {
		if h.Path == `C:\Users\flycat666\Documents\car_home_backup` {
			found = true
		}
	}
	if !found {
		t.Fatal("car_home_backup not found")
	}

	// Case-insensitive.
	hits, _ = ix.Search("CAR_HOME", 20)
	if len(hits) == 0 || hits[0].Path != `D:\project\car_home` {
		t.Fatalf("case-insensitive search failed: %v", hits)
	}

	// Path-only match (no base name contains "project" except the exact one).
	hits, _ = ix.Search("project", 20)
	if len(hits) == 0 || hits[0].Path != `D:\project` {
		t.Fatalf("project search = %v", hits)
	}

	// No match.
	hits, _ = ix.Search("definitely_not_there_xyz", 20)
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %v", hits)
	}
}

func TestDirIndexSearchEmptyQuery(t *testing.T) {
	ix := newTestIndex()
	hits, _ := ix.Search("", 20)
	if len(hits) == 0 {
		t.Fatal("expected top-level folders")
	}
	for _, h := range hits {
		if h.Parent != `D:\` && h.Parent != `C:\` {
			t.Fatalf("empty query returned non-top-level %q (parent %q)", h.Path, h.Parent)
		}
	}
}

func TestDirIndexSearchLimit(t *testing.T) {
	ix := newTestIndex()
	hits, _ := ix.Search("", 3)
	if len(hits) != 3 {
		t.Fatalf("limit 3 returned %d hits", len(hits))
	}
}
