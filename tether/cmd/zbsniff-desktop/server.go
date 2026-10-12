package main

// The zbsniff host runs as a child process — the exact same binary the
// terminal workflow uses (cmd/zbsniff), unchanged. This shell only decides
// where it runs (data directory, HTTP port), starts and stops it, and points
// the window at its dashboard.

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// preferredPort is tried first so the dashboard keeps one origin (and with
// it the browser-side display preferences it stores) from launch to launch;
// 8080 is left to the CLI so both can run at once.
const preferredPort = 8787

type server struct {
	bin, dataDir, logPath string
	port                  int
	cmd                   *exec.Cmd
	logFile               *os.File

	ready  chan struct{} // closed once the dashboard answers
	exited chan struct{} // closed when the child process ends
	err    error         // why it exited (valid after exited is closed)

	stopOnce sync.Once
	stopping atomic.Bool // stop() was requested, so an exit isn't a crash
}

// envOr reads a setting from the environment: a double-clicked app has no
// command line, so ZBSNIFF_* variables are the override hook.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// findBinary locates the zbsniff host: $ZBSNIFF_BIN, next to this executable
// (inside the .app bundle — see build.sh), the tether directory when running
// from a source checkout, then $PATH.
func findBinary() (string, error) {
	name := "zbsniff"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var candidates []string
	if v := os.Getenv("ZBSNIFF_BIN"); v != "" {
		candidates = append(candidates, v)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, name))
		// cmd/zbsniff-desktop/build/bin/<app>[.app/Contents/MacOS] → tether/
		for d, i := dir, 0; i < 10; d, i = filepath.Dir(d), i+1 {
			candidates = append(candidates, filepath.Join(d, "tether", name), filepath.Join(d, name))
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("can't find the %s host binary — build it (go build -o zbsniff ./cmd/zbsniff) "+
		"or set ZBSNIFF_BIN", name)
}

func dataDir() (string, error) {
	dir := os.Getenv("ZBSNIFF_DATA")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "zbsniff-desktop")
	}
	return dir, os.MkdirAll(dir, 0o755)
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func pickPort() (int, error) {
	if v := os.Getenv("ZBSNIFF_HTTP_PORT"); v != "" {
		return strconv.Atoi(v)
	}
	if portFree(preferredPort) {
		return preferredPort, nil
	}
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startServer launches the host and returns immediately; watch s.ready and
// s.exited for the outcome.
func startServer() (*server, error) {
	bin, err := findBinary()
	if err != nil {
		return nil, err
	}
	dir, err := dataDir()
	if err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	port, err := pickPort()
	if err != nil {
		return nil, fmt.Errorf("pick HTTP port: %w", err)
	}
	s := &server{bin: bin, dataDir: dir, port: port, logPath: filepath.Join(dir, "zbsniff.log"),
		ready: make(chan struct{}), exited: make(chan struct{})}

	args := []string{
		"--http-port", strconv.Itoa(port),
		"--config", filepath.Join(dir, "zbsniff.yaml"),
		"--db", filepath.Join(dir, "zbsniff.sqlite"),
	}
	if v := strings.ToLower(os.Getenv("ZBSNIFF_DEMO")); v == "1" || v == "true" || v == "yes" {
		args = append(args, "--demo")
	}
	args = append(args, strings.Fields(os.Getenv("ZBSNIFF_ARGS"))...)

	if s.logFile, err = os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	fmt.Fprintf(s.logFile, "\n==== %s  zbsniff-desktop starting %s %s\n",
		time.Now().Format(time.RFC3339), bin, strings.Join(args, " "))
	s.cmd = exec.Command(bin, args...)
	s.cmd.Dir = dir
	s.cmd.Stdout, s.cmd.Stderr = s.logFile, s.logFile
	if err := s.cmd.Start(); err != nil {
		s.logFile.Close()
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go func() {
		s.err = s.cmd.Wait()
		s.logFile.Close()
		close(s.exited)
	}()
	go s.waitReady()
	return s, nil
}

func (s *server) url() string { return fmt.Sprintf("http://localhost:%d", s.port) }

func (s *server) waitReady() {
	c := http.Client{Timeout: time.Second}
	for {
		select {
		case <-s.exited:
			return
		default:
		}
		if resp, err := c.Get(s.url() + "/api/about"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				close(s.ready)
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stop ends the host: SIGTERM, then a hard kill if it hasn't exited in 5 s.
func (s *server) stop() {
	s.stopOnce.Do(func() {
		s.stopping.Store(true)
		select {
		case <-s.exited:
			return
		default:
		}
		if runtime.GOOS == "windows" {
			_ = s.cmd.Process.Kill()
		} else {
			_ = s.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-s.exited:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
	})
}

// logTail returns the last n lines of the host's log, for error messages.
func (s *server) logTail(n int) string {
	f, err := os.Open(s.logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}

func (s *server) exitReason() string {
	var ee *exec.ExitError
	if errors.As(s.err, &ee) {
		return ee.String()
	}
	if s.err != nil {
		return s.err.Error()
	}
	return "exited"
}
