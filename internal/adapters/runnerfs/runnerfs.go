// Package runnerfs reads what a GitHub Actions self-hosted runner leaves on
// disk: which repo it serves (.runner), whether it is running a job right
// now (_diag/Runner_*.log), and the running step's output (_diag/pages).
// It only reads; nothing in a runner's folder is ever changed.
package runnerfs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"runwatch/internal/domain"
)

// tailBytes is how much of the end of a Runner log is read. A busy runner
// adds a "renew job" line a minute, so this covers many hours of one job.
const tailBytes = 512 << 10

// Discover returns the runner folders under roots (a root's children that
// hold a .runner file, or the root itself) plus the extra folders given,
// without duplicates, sorted.
func Discover(roots, extra []string) ([]string, error) {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		key := strings.ToLower(filepath.Clean(dir))
		if !seen[key] && isRunner(dir) {
			seen[key] = true
			dirs = append(dirs, filepath.Clean(dir))
		}
	}
	var errs []error
	for _, root := range roots {
		add(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			errs = append(errs, fmt.Errorf("runner folder %s: %w", root, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(root, e.Name()))
			}
		}
	}
	for _, dir := range extra {
		add(dir)
	}
	sort.Strings(dirs)
	return dirs, errors.Join(errs...)
}

func isRunner(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".runner"))
	return err == nil
}

// Read fills a Runner from its folder: name and repo from .runner, the
// running job from the newest Runner log. Service state is not known here.
func Read(dir string) (domain.Runner, error) {
	r := domain.Runner{Dir: dir, Name: filepath.Base(dir), State: domain.ServiceUnknown}
	raw, err := os.ReadFile(filepath.Join(dir, ".runner"))
	if err != nil {
		return r, fmt.Errorf("read .runner: %w", err)
	}
	var cfg struct {
		AgentName string `json:"agentName"`
		GitHubURL string `json:"gitHubUrl"`
	}
	// The runner writes .runner with a UTF-8 byte order mark.
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &cfg); err != nil {
		return r, fmt.Errorf("parse .runner: %w", err)
	}
	if cfg.AgentName != "" {
		r.Name = cfg.AgentName
	}
	r.Repo = repoOf(cfg.GitHubURL)
	if r.Repo == "" {
		return r, fmt.Errorf(".runner has no repository URL (gitHubUrl=%q); organization runners are not supported", cfg.GitHubURL)
	}
	log, err := newestLog(filepath.Join(dir, "_diag"))
	if err != nil {
		return r, err
	}
	if log == "" {
		return r, nil // never started: not busy
	}
	tail, err := readTail(log, tailBytes)
	if err != nil {
		return r, fmt.Errorf("read %s: %w", filepath.Base(log), err)
	}
	st := ParseRunnerLog(tail)
	r.Busy, r.JobName, r.JobStarted, r.PlanID = st.Busy, st.JobName, st.Started, st.PlanID
	return r, nil
}

// repoOf turns https://github.com/owner/repo into owner/repo; "" when the
// URL names an organization or is malformed.
func repoOf(url string) string {
	i := strings.Index(url, "://")
	if i < 0 {
		return ""
	}
	parts := strings.Split(strings.Trim(url[i+3:], "/"), "/")
	if len(parts) != 3 {
		return ""
	}
	return parts[1] + "/" + parts[2]
}

func newestLog(diag string) (string, error) {
	names, err := filepath.Glob(filepath.Join(diag, "Runner_*.log"))
	if err != nil {
		return "", fmt.Errorf("list runner logs: %w", err)
	}
	if len(names) == 0 {
		if _, err := os.Stat(diag); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("open _diag: %w", err)
		}
		return "", nil
	}
	sort.Strings(names) // the name carries the start time
	return names[len(names)-1], nil
}

func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > n {
		if _, err := f.Seek(-n, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// LogState is what a Runner log says about the job in hand.
type LogState struct {
	Busy    bool
	JobName string
	Started time.Time
	PlanID  string // names the job's files in _diag/pages
}

var (
	runningRe = regexp.MustCompile(`WRITE LINE: (\d{4}-\d\d-\d\d \d\d:\d\d:\d\dZ): Running job: (.+?)\r?$`)
	doneRe    = regexp.MustCompile(`WRITE LINE: \d{4}-\d\d-\d\d \d\d:\d\d:\d\dZ: Job .+ completed with result: `)
	planRe    = regexp.MustCompile(`Job request \d+ for plan ([0-9a-f-]{36}) job [0-9a-f-]{36} received`)
)

// ParseRunnerLog reads the end of a Runner_*.log: the last "Running job"
// line with no "completed" line after it means the runner is busy.
func ParseRunnerLog(tail []byte) LogState {
	var st LogState
	plan := ""
	for line := range strings.SplitSeq(string(tail), "\n") {
		switch {
		case planRe.MatchString(line):
			plan = planRe.FindStringSubmatch(line)[1]
		case runningRe.MatchString(line):
			m := runningRe.FindStringSubmatch(line)
			at, err := time.Parse("2006-01-02 15:04:05Z", m[1])
			if err != nil {
				continue
			}
			st = LogState{Busy: true, JobName: m[2], Started: at, PlanID: plan}
		case doneRe.MatchString(line):
			st = LogState{}
		}
	}
	return st
}

// Sizes walks a runner's _diag and _work folders and returns their sizes in
// bytes. It can take seconds on a big checkout: call it rarely.
func Sizes(dir string) (diag, work int64, err error) {
	diag, err1 := dirSize(filepath.Join(dir, "_diag"))
	work, err2 := dirSize(filepath.Join(dir, "_work"))
	return diag, work, errors.Join(err1, err2)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// Files come and go under a running job; a missing one is not a failure.
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return total, fmt.Errorf("measure %s: %w", root, err)
	}
	return total, nil
}
