package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runwatch/internal/domain"
)

var t0 = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

func job(id int64, minsAgo int) domain.Job {
	return domain.Job{ID: id, Repo: "example/app", Name: "unit", Status: domain.StatusCompleted, Conclusion: domain.ConclusionSuccess,
		Runner: "app-1", StartedAt: t0.Add(-time.Duration(minsAgo+2) * time.Minute), FinishedAt: t0.Add(-time.Duration(minsAgo) * time.Minute)}
}

func ids(jobs []domain.Job) []int64 {
	var out []int64
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	return out
}

func TestAddKeepsFinishedJobsOnceNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "history.jsonl") // the folder does not exist yet
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	running := domain.Job{ID: 9, Repo: "example/app", Status: domain.StatusInProgress}
	added, err := s.Add([]domain.Job{job(1, 30), running, job(2, 10)})
	if err != nil || len(added) != 2 {
		t.Fatalf("added = %v, %v; want the two finished jobs", ids(added), err)
	}
	if added, err = s.Add([]domain.Job{job(1, 30), job(3, 20)}); err != nil || len(added) != 1 || added[0].ID != 3 {
		t.Fatalf("second add = %v, %v; want only job 3", ids(added), err)
	}
	// The same id in another repo is another job.
	other := job(1, 5)
	other.Repo = "acme/billing"
	if added, err = s.Add([]domain.Job{other}); err != nil || len(added) != 1 {
		t.Fatalf("other repo: %v, %v", ids(added), err)
	}
	if got := ids(s.Jobs()); len(got) != 4 || got[0] != 1 || got[1] != 2 || got[2] != 3 || got[3] != 1 {
		t.Errorf("order = %v, want newest finished first", got)
	}
	if !s.Has("example/app", 2) || s.Has("example/app", 9) {
		t.Error("Has")
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Jobs(); len(got) != 4 || got[0].Repo != "acme/billing" || !got[1].FinishedAt.Equal(t0.Add(-10*time.Minute)) {
		t.Errorf("reloaded = %+v", got)
	}
	if added, _ := again.Add([]domain.Job{job(2, 10)}); len(added) != 0 {
		t.Error("a reloaded store must still know its jobs")
	}
}

func TestOpenSurvivesATornLineAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, _ := Open(path)
	if _, err := s.Add([]domain.Job{job(1, 30), job(2, 10)}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// The app was killed mid-write, and an old version wrote job 1 twice.
	if _, err := f.WriteString(`{"id":1,"repo":"example/app","status":"completed"}` + "\n" + `{"id":3,"repo":"exam`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(again.Jobs()); len(got) != 2 {
		t.Fatalf("jobs = %v, want 1 and 2", got)
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "\n"); n != 2 || strings.Contains(string(raw), `"exam`+"\n") {
		t.Errorf("the file was not rewritten clean: %d lines\n%s", n, raw)
	}
}

func TestOpenDropsTheOldestBeyondTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, _ := Open(path)
	var jobs []domain.Job
	for i := range keep + 5 {
		jobs = append(jobs, job(int64(i+1), i)) // id 1 is the newest
	}
	if _, err := s.Add(jobs); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Jobs()
	if len(got) != keep || got[0].ID != 1 || got[keep-1].ID != keep {
		t.Fatalf("kept %d jobs, first %d, last %d", len(got), got[0].ID, got[len(got)-1].ID)
	}
	if again.Has("example/app", keep+1) {
		t.Error("a dropped job is still known")
	}
}

func TestOpenAndAddReportUnusablePaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil { // a folder where the file should be
		t.Error("Open on a folder: want an error")
	}
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(blocked, "history.jsonl")) // a file where the folder should be
	if err != nil {
		// Some systems refuse at Open already; that is as good.
		return
	}
	if _, err := s.Add([]domain.Job{job(1, 1)}); err == nil {
		t.Error("Add must report that it could not save")
	}
	if len(s.Jobs()) != 0 {
		t.Error("a job that was not saved must not be kept in memory")
	}
}
