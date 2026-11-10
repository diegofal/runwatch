package runnerfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"runwatch/internal/domain"
)

// Tail follows the output of one running job. The runner writes each step's
// output to _diag/pages/<plan>_<record>_<n>.log in blocks of a few KB,
// uploads the file and deletes it seconds later, so Tail must be polled
// several times a second and keeps every line it has seen. Output that is
// written and deleted between two polls is lost: the live view is best
// effort, the full log comes from GitHub once the job ends.
type Tail struct {
	dir     string // the runner's _diag/pages
	plan    string
	since   time.Time
	offsets map[string]int64
	partial map[string]string
	last    time.Time
}

// NewTail follows the job of plan that started at since in the runner
// folder dir. Files last written before since belong to an earlier job of
// the same run and are skipped.
func NewTail(dir, plan string, since time.Time) *Tail {
	return &Tail{
		dir: filepath.Join(dir, "_diag", "pages"), plan: plan, since: since.Add(-2 * time.Second),
		offsets: map[string]int64{}, partial: map[string]string{}, last: since,
	}
}

// Poll returns the complete lines written since the last call, oldest first.
func (t *Tail) Poll() ([]domain.LogLine, error) {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []domain.LogLine
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, t.plan+"_") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(t.since) || info.Size() <= t.offsets[name] {
			continue // gone already, an earlier job's, or nothing new
		}
		lines, err := t.read(name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		out = append(out, lines...)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].At.Before(out[b].At) })
	return out, errors.Join(errs...)
}

func (t *Tail) read(name string) ([]domain.LogLine, error) {
	f, err := os.Open(filepath.Join(t.dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	if _, err := f.Seek(t.offsets[name], io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	t.offsets[name] += int64(len(raw))
	text := t.partial[name] + string(raw)
	cut := strings.LastIndexByte(text, '\n')
	if cut < 0 {
		t.partial[name] = text
		return nil, nil
	}
	t.partial[name] = text[cut+1:]
	var out []domain.LogLine
	for line := range strings.SplitSeq(text[:cut], "\n") {
		l := domain.ParseLogLine(strings.TrimSuffix(line, "\r"), t.last)
		t.last = l.At
		out = append(out, l)
	}
	return out, nil
}
