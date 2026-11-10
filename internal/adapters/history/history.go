// Package history keeps the finished jobs Runwatch has seen, one JSON
// object per line in history.jsonl. It is what the History tab lists and
// what "usual" run times are the median of.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"runwatch/internal/domain"
)

// keep is how many finished jobs are kept; older ones are dropped when the
// file is rewritten at start.
const keep = 5000

// Store is the finished-jobs file plus its contents in memory.
type Store struct {
	path string
	mu   sync.Mutex
	jobs []domain.Job // newest first
	seen map[string]bool
}

func key(j domain.Job) string { return fmt.Sprintf("%s:%d", j.Repo, j.ID) }

// Open loads path (a missing file is an empty history) and compacts it to
// the newest keep jobs.
func Open(path string) (*Store, error) {
	s := &Store{path: path, seen: map[string]bool{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	lines := 0
	for sc.Scan() {
		lines++
		var j domain.Job
		// A torn last line (the app was killed mid-write) is skipped: the
		// job will be seen again on the next poll if it is recent.
		if json.Unmarshal(sc.Bytes(), &j) != nil || j.ID == 0 {
			continue
		}
		if !s.seen[key(j)] {
			s.seen[key(j)] = true
			s.jobs = append(s.jobs, j)
		}
	}
	err = errors.Join(sc.Err(), f.Close())
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	s.sort()
	if len(s.jobs) > keep || lines != len(s.jobs) {
		if len(s.jobs) > keep {
			for _, j := range s.jobs[keep:] {
				delete(s.seen, key(j))
			}
			s.jobs = s.jobs[:keep]
		}
		if err := s.rewrite(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) sort() {
	sort.SliceStable(s.jobs, func(a, b int) bool { return s.jobs[a].FinishedAt.After(s.jobs[b].FinishedAt) })
}

func (s *Store) rewrite() error {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("rewrite history: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i := len(s.jobs) - 1; i >= 0; i-- { // oldest first, as appended
		if err := enc.Encode(s.jobs[i]); err != nil {
			f.Close() //nolint:errcheck // the encode error is the one reported
			return fmt.Errorf("rewrite history: %w", err)
		}
	}
	if err := errors.Join(w.Flush(), f.Close()); err != nil {
		return fmt.Errorf("rewrite history: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rewrite history: %w", err)
	}
	return nil
}

// Has reports whether the job is already stored.
func (s *Store) Has(repo string, id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[fmt.Sprintf("%s:%d", repo, id)]
}

// Add stores finished jobs not stored yet and returns the ones added.
func (s *Store) Add(jobs []domain.Job) ([]domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var added []domain.Job
	for _, j := range jobs {
		if j.Status == domain.StatusCompleted && !s.seen[key(j)] {
			added = append(added, j)
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("save history: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("save history: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, j := range added {
		if err := enc.Encode(j); err != nil {
			f.Close() //nolint:errcheck // the encode error is the one reported
			return nil, fmt.Errorf("save history: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("save history: %w", err)
	}
	for _, j := range added {
		s.seen[key(j)] = true
		s.jobs = append(s.jobs, j)
	}
	s.sort()
	return added, nil
}

// Jobs returns the stored jobs, newest first. The slice is a copy.
func (s *Store) Jobs() []domain.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Job(nil), s.jobs...)
}
