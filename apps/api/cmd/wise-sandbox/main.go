// Command wise-sandbox runs inside a task sandbox container. The executor
// calls it through `docker exec` to work on the task workspace: file
// operations (JSON on stdin and stdout), commands with a timeout that can be
// killed as a process group, and an idle loop that keeps the container alive.
//
// Everything in the sandbox is untrusted, this helper included: the executor
// treats its output as data. Path confinement here keeps agents' file tools
// inside the workspace; the container is the security boundary.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"wise/apps/api/internal/secretguard"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wise-sandbox idle|fs|run|kill")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "idle":
		idle()
	case "fs":
		os.Exit(fsMain(os.Stdin, os.Stdout))
	case "run":
		os.Exit(runMain(os.Args[2:]))
	case "kill":
		os.Exit(killMain(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

func idle() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}

// ---- file operations ----

// Request is one file operation on the workspace.
type Request struct {
	Op       string `json:"op"` // list | read | write | edit | delete | search
	Path     string `json:"path,omitempty"`
	Content  string `json:"content,omitempty"`
	Old      string `json:"old,omitempty"`
	New      string `json:"new,omitempty"`
	Query    string `json:"query,omitempty"`
	Regex    bool   `json:"regex,omitempty"`
	Glob     string `json:"glob,omitempty"`
	Start    int    `json:"start,omitempty"` // read: first line (1-based)
	Lines    int    `json:"lines,omitempty"` // read: line count (0 = to the end)
	Limit    int    `json:"limit,omitempty"`
	MaxBytes int    `json:"maxBytes,omitempty"`
}

// Response is the result of a file operation.
type Response struct {
	Error      string   `json:"error,omitempty"`
	Content    string   `json:"content,omitempty"`
	TotalLines int      `json:"totalLines,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
	Entries    []Entry  `json:"entries,omitempty"`
	Matches    []Match  `json:"matches,omitempty"`
	Bytes      int      `json:"bytes,omitempty"`
	Created    bool     `json:"created,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// Entry is a listed file or directory.
type Entry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// Match is a search hit.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func root() string {
	if r := os.Getenv("WISE_WORKSPACE"); r != "" {
		return r
	}
	return "/workspace"
}

var errDenied = errors.New("denied")

// resolve maps a workspace-relative path to an absolute one inside the
// workspace, following symlinks only while they stay inside. forWrite also
// refuses the .git directory.
func resolve(rel string, forWrite bool) (string, string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." || rel == "/" {
		rel = "."
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))[1:]
	if clean == "" {
		clean = "."
	}
	if strings.Contains(rel, "\x00") {
		return "", "", fmt.Errorf("%w: invalid path", errDenied)
	}
	if forWrite && (clean == ".git" || strings.HasPrefix(clean, ".git/")) {
		return "", "", fmt.Errorf("%w: the .git directory cannot be changed with file tools", errDenied)
	}
	base, err := filepath.EvalSymlinks(root())
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(base, clean)
	// Resolve the deepest existing ancestor and make sure it stays inside.
	probe := abs
	for {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			rest, _ := filepath.Rel(probe, abs)
			full := filepath.Join(real, rest)
			if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
				return "", "", fmt.Errorf("%w: %s points outside the workspace", errDenied, clean)
			}
			if forWrite {
				r, _ := filepath.Rel(base, full)
				if r == ".git" || strings.HasPrefix(r, ".git"+string(os.PathSeparator)) {
					return "", "", fmt.Errorf("%w: the .git directory cannot be changed with file tools", errDenied)
				}
			}
			return full, clean, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", "", fmt.Errorf("%w: bad path", errDenied)
		}
		probe = parent
	}
}

func secretErr(clean string) error {
	return fmt.Errorf("%w: %s may contain secrets and is not shown", errDenied, clean)
}

func fsMain(in io.Reader, out io.Writer) int {
	var req Request
	dec := json.NewDecoder(io.LimitReader(in, 16<<20))
	if err := dec.Decode(&req); err != nil {
		return reply(out, Response{Error: "bad request: " + err.Error()})
	}
	res, err := do(req)
	if err != nil {
		res = Response{Error: err.Error()}
	}
	return reply(out, res)
}

func reply(out io.Writer, r Response) int {
	_ = json.NewEncoder(out).Encode(r)
	return 0
}

func do(req Request) (Response, error) {
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}
	switch req.Op {
	case "list":
		return list(req.Path, req.Glob, limit)
	case "read":
		return read(req, maxBytes)
	case "write":
		return write(req.Path, req.Content)
	case "edit":
		return edit(req.Path, req.Old, req.New)
	case "delete":
		return remove(req.Path)
	case "search":
		return search(req, limit)
	}
	return Response{}, fmt.Errorf("unknown op %q", req.Op)
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"__pycache__": true, "dist": true, "build": true, "target": true, ".next": true, "coverage": true}

func list(rel, glob string, limit int) (Response, error) {
	abs, clean, err := resolve(rel, false)
	if err != nil {
		return Response{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Response{}, cleanErr(err, clean)
	}
	if !st.IsDir() {
		return Response{Entries: []Entry{{Path: clean, Size: st.Size()}}}, nil
	}
	var res Response
	base, _ := filepath.EvalSymlinks(root())
	if glob == "" {
		// One level, directories first.
		items, err := os.ReadDir(abs)
		if err != nil {
			return Response{}, cleanErr(err, clean)
		}
		for _, it := range items {
			if it.Name() == ".git" {
				continue
			}
			p := path.Join(clean, it.Name())
			if clean == "." {
				p = it.Name()
			}
			e := Entry{Path: p, Dir: it.IsDir()}
			if info, err := it.Info(); err == nil && !it.IsDir() {
				e.Size = info.Size()
			}
			res.Entries = append(res.Entries, e)
		}
		sort.SliceStable(res.Entries, func(i, j int) bool { return res.Entries[i].Dir && !res.Entries[j].Dir })
	} else {
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != abs && skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			r, _ := filepath.Rel(base, p)
			r = filepath.ToSlash(r)
			if ok, _ := path.Match(glob, path.Base(r)); ok || matchPath(glob, r) {
				if len(res.Entries) >= limit {
					res.Truncated = true
					return filepath.SkipAll
				}
				info, _ := d.Info()
				var size int64
				if info != nil {
					size = info.Size()
				}
				res.Entries = append(res.Entries, Entry{Path: r, Size: size})
			}
			return nil
		})
		if err != nil {
			return Response{}, err
		}
	}
	if len(res.Entries) > limit {
		res.Entries, res.Truncated = res.Entries[:limit], true
	}
	return res, nil
}

// matchPath supports "**/" prefixes on top of path.Match.
func matchPath(glob, p string) bool {
	if ok, _ := path.Match(glob, p); ok {
		return true
	}
	if rest, ok := strings.CutPrefix(glob, "**/"); ok {
		parts := strings.Split(p, "/")
		for i := range parts {
			if ok, _ := path.Match(rest, strings.Join(parts[i:], "/")); ok {
				return true
			}
		}
	}
	return false
}

func cleanErr(err error, clean string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist", clean)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %v", clean, pe.Err)
	}
	return err
}

func read(req Request, maxBytes int) (Response, error) {
	abs, clean, err := resolve(req.Path, false)
	if err != nil {
		return Response{}, err
	}
	if secretguard.LooksSecret(clean) || secretguard.LooksSecret(abs) {
		return Response{}, secretErr(clean)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Response{}, cleanErr(err, clean)
	}
	if st.IsDir() {
		return Response{}, fmt.Errorf("%s is a directory; use list_files", clean)
	}
	if st.Size() > 8<<20 {
		return Response{}, fmt.Errorf("%s is too large to read (%d bytes)", clean, st.Size())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Response{}, cleanErr(err, clean)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return Response{}, fmt.Errorf("%s is a binary file", clean)
	}
	text := string(data)
	if secretguard.HasToken(text) {
		return Response{}, secretErr(clean)
	}
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	res := Response{TotalLines: len(lines)}
	start := max(req.Start, 1)
	end := len(lines)
	if req.Lines > 0 {
		end = min(end, start-1+req.Lines)
	}
	var b strings.Builder
	for i := start - 1; i < end && i < len(lines); i++ {
		if b.Len()+len(lines[i]) > maxBytes {
			res.Truncated = true
			break
		}
		b.WriteString(lines[i])
	}
	if end < len(lines) {
		res.Truncated = true
	}
	res.Content = b.String()
	return res, nil
}

func write(rel, content string) (Response, error) {
	abs, clean, err := resolve(rel, true)
	if err != nil {
		return Response{}, err
	}
	if clean == "." {
		return Response{}, errors.New("a file path is required")
	}
	created := false
	if st, err := os.Lstat(abs); err == nil {
		if st.IsDir() {
			return Response{}, fmt.Errorf("%s is a directory", clean)
		}
	} else {
		created = true
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Response{}, cleanErr(err, clean)
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(abs); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".wise-write-*")
	if err != nil {
		return Response{}, cleanErr(err, clean)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return Response{}, err
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), abs); err != nil {
		os.Remove(tmp.Name())
		return Response{}, cleanErr(err, clean)
	}
	return Response{Bytes: len(content), Created: created}, nil
}

func edit(rel, old, repl string) (Response, error) {
	if old == "" {
		return Response{}, errors.New("old text is required (use write_file to create a file)")
	}
	abs, clean, err := resolve(rel, true)
	if err != nil {
		return Response{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Response{}, cleanErr(err, clean)
	}
	text := string(data)
	switch n := strings.Count(text, old); n {
	case 0:
		return Response{}, fmt.Errorf("the old text was not found in %s; read the file and copy the text exactly", clean)
	case 1:
	default:
		return Response{}, fmt.Errorf("the old text occurs %d times in %s; include more surrounding lines so it is unique", n, clean)
	}
	return write(rel, strings.Replace(text, old, repl, 1))
}

func remove(rel string) (Response, error) {
	abs, clean, err := resolve(rel, true)
	if err != nil {
		return Response{}, err
	}
	if clean == "." {
		return Response{}, errors.New("cannot delete the workspace")
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return Response{}, cleanErr(err, clean)
	}
	if st.IsDir() {
		return Response{}, fmt.Errorf("%s is a directory; delete its files one by one", clean)
	}
	return Response{}, os.Remove(abs)
}

func search(req Request, limit int) (Response, error) {
	if strings.TrimSpace(req.Query) == "" {
		return Response{}, errors.New("query is required")
	}
	var match func(string) bool
	if req.Regex {
		re, err := regexp.Compile(req.Query)
		if err != nil {
			return Response{}, fmt.Errorf("bad regex: %v", err)
		}
		match = re.MatchString
	} else {
		q := strings.ToLower(req.Query)
		match = func(s string) bool { return strings.Contains(strings.ToLower(s), q) }
	}
	abs, _, err := resolve(req.Path, false)
	if err != nil {
		return Response{}, err
	}
	base, _ := filepath.EvalSymlinks(root())
	var res Response
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != abs && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		r, _ := filepath.Rel(base, p)
		r = filepath.ToSlash(r)
		if req.Glob != "" {
			if ok, _ := path.Match(req.Glob, path.Base(r)); !ok && !matchPath(req.Glob, r) {
				return nil
			}
		}
		if secretguard.LooksSecret(r) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		n := 0
		for sc.Scan() {
			n++
			line := sc.Text()
			if bytes.IndexByte([]byte(line), 0) >= 0 {
				return nil // binary
			}
			if match(line) {
				if len(res.Matches) >= limit {
					res.Truncated = true
					return filepath.SkipAll
				}
				res.Matches = append(res.Matches, Match{Path: r, Line: n, Text: secretguard.Redact(clip(strings.TrimSpace(line), 300))})
			}
		}
		return nil
	})
	return res, err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- commands ----

func jobsDir() string { return filepath.Join(os.TempDir(), "wise-jobs") }

var jobID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// runMain runs `sh -c <command>` in its own process group with combined
// output on stdout. Exit codes: the command's, 124 on timeout.
func runMain(args []string) int {
	var id, cmd string
	timeout := 600
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			i++
			id = args[i]
		case "--timeout":
			i++
			timeout, _ = strconv.Atoi(args[i])
		case "--":
			cmd = strings.Join(args[i+1:], " ")
			i = len(args)
		}
	}
	if !jobID.MatchString(id) || cmd == "" {
		fmt.Fprintln(os.Stderr, "usage: wise-sandbox run --id ID --timeout S -- command")
		return 2
	}
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Stdout, c.Stderr = os.Stdout, os.Stdout
	c.Stdin = nil
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		fmt.Println("could not start:", err)
		return 127
	}
	_ = os.MkdirAll(jobsDir(), 0o700)
	pidFile := filepath.Join(jobsDir(), id)
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(c.Process.Pid)), 0o600)
	defer os.Remove(pidFile)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ee.ExitCode()
		}
		if err != nil {
			return 1
		}
		return 0
	case <-time.After(time.Duration(max(timeout, 1)) * time.Second):
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		fmt.Printf("\n[wise] command timed out after %ds and was stopped\n", timeout)
		return 124
	}
}

// killMain stops a running command's process group.
func killMain(args []string) int {
	if len(args) != 1 || !jobID.MatchString(args[0]) {
		return 2
	}
	data, err := os.ReadFile(filepath.Join(jobsDir(), args[0]))
	if err != nil {
		return 0 // already finished
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 1
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return 0
}
