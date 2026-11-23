package main

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"
)

func TestSourceFileAndMarkers(t *testing.T) {
	for p, want := range map[string]bool{
		"main.go": true, "web/App.vue": true, "a/b.SCSS": true, "x.tsx": true, "setup.py": false, "tools/setup.py": false,
		"DockerfileParser.java": true, "lib/Makefile.am": false, "docs/makefile.png": false, "Dockerfile.j2": false, "web/Dockerfile.html": false, "Makefile": false, "data.json": false, "query.sql": false, ".bashrc": false, "README.md": false,
	} {
		if got := sourceFile(p); got != want {
			t.Errorf("sourceFile(%q) = %v, want %v", p, got, want)
		}
	}
	m := map[string]bool{}
	for _, p := range []string{"Dockerfile", "deploy/Dockerfile.prod", "infra/main.tf", "charts/app/Chart.yaml", "web/App.vue",
		"android/app/src/main/AndroidManifest.xml", "ios/Dockerfile", "src/main.go", "CMakeLists.txt",
		"vendor/x/helmfile.yaml", "Examples/y/bun.lockb", "node_modules/z/Widget.svelte"} {
		addMarkers(m, p)
	}
	if got, want := slices.Sorted(maps.Keys(m)), []string{"*.svelte", "*.tf", "*.vue", "chart.yaml", "cmakelists.txt", "dockerfile"}; !slices.Equal(got, want) {
		t.Errorf("markers = %v, want %v", got, want)
	}
	m = map[string]bool{} // code, prose and media named like a Dockerfile are not one
	for _, p := range []string{"docs/dockerfile.md", "src/DockerfileParser.java", "img/dockerfile.png"} {
		addMarkers(m, p)
	}
	if len(m) > 0 {
		t.Errorf("markers = %v, want none", m)
	}
}

// TestSnapshots folds stored commits of two repositories, one a mirror of the other, into snapshots.
func TestSnapshots(t *testing.T) {
	repos := []Repo{
		{ID: "b", Provider: "GITHUB", Namespace: "me/mirror"},
		{ID: "a", Provider: "GITHUB", Namespace: "me/app", AsOf: "2026-09-03T00:00:00Z",
			Churn: map[string]churn{"2026-09-02": {Touches: pair{2, 1}, Corrective: pair{1}}}},
		{ID: "c"}, // no git service remote: never sent
	}
	commits := []Commit{
		{Repo: "a", Hash: "1", Date: "2026-09-01T23:30:00-02:00", Branch: "main", Message: "feat: login", CreationType: creationHuman,
			Files: []FileStat{{Name: "src/login.go", Created: true}, {Name: "src/main.go"}, {Name: "db/schema.sql"}, {Name: "Dockerfile"}}},
		{Repo: "a", Hash: "2", Date: "2026-09-02T10:00:00+09:00", Branch: "main", Message: "fix login", CreationType: creationHumanCoAI,
			Files: []FileStat{{Name: "web/Login.vue", Created: true}}},
		{Repo: "a", Hash: "3", Date: "2026-09-02T10:00:00+09:00", Branch: "main", Message: "fix login", CreationType: creationHumanCoAI, // amended copy
			Files: []FileStat{{Name: "web/Login.vue", Created: true}}},
		{Repo: "a", Hash: "4", Date: "2026-09-02T11:00:00+09:00", Branch: "main", Message: "Merge branch 'x'", CreationType: creationHuman,
			Files: []FileStat{{Name: "infra/main.tf", Created: true}, {Name: "a.py"}}},
		{Repo: "a", Hash: "5", Date: "2026-09-02T12:00:00+09:00", Branch: "main", Message: "update docs", CreationType: creationHuman,
			Files: []FileStat{{Name: "README.md"}}},
		{Repo: "b", Hash: "1", Date: "2026-09-02T01:30:00Z", Branch: "main", Message: "feat: login", CreationType: creationHuman,
			Files: []FileStat{{Name: "src/login.go", Created: true}}},
		{Repo: "a", Hash: "7", Date: "2026-09-03T10:00:00Z", Branch: "main", Message: "feat: login again", CreationType: creationHuman, // made again: counts once
			Files: []FileStat{{Name: "src/login.go", Created: true}}},
		{Repo: "a", Hash: "6", Date: "2026-09-04T10:00:00Z", Message: "feat: not pushed yet", CreationType: creationHuman,
			Files: []FileStat{{Name: "src/next.go", Created: true}}},
		{Repo: "c", Hash: "9", Date: "2026-09-02T01:30:00Z", Message: "feat: x", CreationType: creationHuman},
	}
	got := snapshots(repos, commits, "me@example.com")
	b, _ := json.Marshal(got)
	want := `[` +
		`{"format":1,"provider":"GITHUB","namespace":"me/app","authorEmail":"me@example.com","asOf":"2026-09-03T10:00:00Z","days":{` +
		`"2026-09-02":{"commits":[2,1],"langs":{"Go":[2,0]},"created":[1,1],"touches":[2,1],"corrective":[1,0],"mine":[1,2],"others":[1,0]},` +
		`"2026-09-03":{"commits":[1,0],"langs":{"Go":[1,0]},"mine":[1,0]}},` +
		`"markers":["*.tf","*.vue","dockerfile"],"stackLangs":["Go","SQL"]},` +
		`{"format":1,"provider":"GITHUB","namespace":"me/mirror","authorEmail":"me@example.com","asOf":"2026-09-02T01:30:00Z","days":{` +
		`"2026-09-02":{"commits":[1,0],"dup":[1,0],"langs":{"Go":[1,0]},"created":[1,0],"mine":[1,0]}},"stackLangs":["Go"]}]`
	if string(b) != want {
		t.Errorf("snapshots =\n%s\nwant\n%s", b, want)
	}
}

// Two clones of one repository make one snapshot: the commits of both (each stored under the one that
// found it first), and what the newer scan counted of others, not the sum.
func TestSnapshotOfCopies(t *testing.T) {
	repos := []Repo{
		{ID: "x", Provider: "GITHUB", Namespace: "me/app", AsOf: "2026-09-01T00:00:00Z", Churn: map[string]churn{"2026-09-01": {Touches: pair{1}}}},
		{ID: "y", Provider: "GITHUB", Namespace: "me/app", AsOf: "2026-09-05T00:00:00Z", Churn: map[string]churn{"2026-09-01": {Touches: pair{3}}}},
	}
	commits := []Commit{
		{Repo: "x", Hash: "1", Date: "2026-09-01T10:00:00Z", Branch: "main", Message: "feat: a", CreationType: creationHuman, Files: []FileStat{{Name: "a.go"}}},
		{Repo: "y", Hash: "2", Date: "2026-09-02T10:00:00Z", Branch: "main", Message: "feat: b", CreationType: creationHuman, Files: []FileStat{{Name: "b.go"}}},
		{Repo: "y", Hash: "1", Date: "2026-09-01T10:00:00Z", Branch: "main", Message: "feat: a", CreationType: creationHuman, Files: []FileStat{{Name: "a.go"}}}, // under both
	}
	got := snapshots(repos, commits, "me@example.com")
	if len(got) != 1 || got[0].Days["2026-09-01"].Commits[0] != 1 || got[0].Days["2026-09-02"].Commits[0] != 1 ||
		got[0].Days["2026-09-01"].Mine[0] != 1 || got[0].Days["2026-09-01"].Langs["Go"][0] != 1 ||
		got[0].Days["2026-09-01"].Touches[0] != 3 || got[0].AsOf != "2026-09-05T00:00:00Z" {
		b, _ := json.Marshal(got)
		t.Errorf("snapshots = %s", b)
	}
}

// A file that happens again counts once: on its first day, but on the first day without an AI signal if it had one.
func TestFirst(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	for _, c := range []struct{ was, now, want origin }{
		{origin{}, origin{day(1), 1}, origin{day(1), 1}},
		{origin{day(1), 1}, origin{day(2), 1}, origin{day(1), 1}},
		{origin{day(2), 1}, origin{day(1), 1}, origin{day(1), 1}}, // read out of order
		{origin{day(1), 1}, origin{day(2), 0}, origin{day(2), 0}}, // without AI wins
		{origin{day(1), 0}, origin{day(2), 1}, origin{day(1), 0}},
		{origin{day(3), 0}, origin{day(1), 0}, origin{day(1), 0}},
	} {
		if got := first(c.was, c.now); got != c.want {
			t.Errorf("first(%v, %v) = %v, want %v", c.was, c.now, got, c.want)
		}
	}
}
