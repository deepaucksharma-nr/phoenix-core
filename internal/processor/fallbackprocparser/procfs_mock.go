package fallbackprocparser

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mockProcFS creates a temporary directory structure that mimics /proc
// for testing purposes.
type mockProcFS struct {
	rootDir    string
	processes  map[int]*mockProcess
	t          *testing.T
	cleanup    func()
}

// mockProcess represents a mocked process in the mock /proc filesystem
type mockProcess struct {
	pid          int
	cmdline      string
	uid          string
	username     string
	execPath     string
	readBytes    float64
	writeBytes   float64
	shouldExist  bool  // Controls if the process "exists"
	accessErrors bool  // Simulates permission errors
}

// newMockProcFS creates a new mock /proc filesystem
func newMockProcFS(t *testing.T) *mockProcFS {
	// Create a temporary directory
	rootDir, err := ioutil.TempDir("", "mockproc")
	require.NoError(t, err, "Failed to create temporary directory")

	mock := &mockProcFS{
		rootDir:   rootDir,
		processes: make(map[int]*mockProcess),
		t:         t,
	}

	// Setup cleanup function to remove the temp directory
	mock.cleanup = func() {
		os.RemoveAll(rootDir)
	}

	return mock
}

// AddProcess adds a mock process to the mock filesystem
func (m *mockProcFS) AddProcess(pid int, opts ...mockProcessOption) {
	proc := &mockProcess{
		pid:         pid,
		cmdline:     fmt.Sprintf("process-%d", pid),
		uid:         "1000",
		username:    "testuser",
		execPath:    fmt.Sprintf("/usr/bin/process-%d", pid),
		readBytes:   1024.0,
		writeBytes:  512.0,
		shouldExist: true,
	}

	// Apply all options
	for _, opt := range opts {
		opt(proc)
	}

	m.processes[pid] = proc
	m.createProcessFiles(proc)
}

// createProcessFiles creates the mock files for a process
func (m *mockProcFS) createProcessFiles(proc *mockProcess) {
	if !proc.shouldExist {
		return
	}

	// Create process directory
	procDir := filepath.Join(m.rootDir, fmt.Sprintf("%d", proc.pid))
	err := os.MkdirAll(procDir, 0755)
	require.NoError(m.t, err, "Failed to create process directory")

	// Create cmdline file
	if !proc.accessErrors {
		cmdlineFile := filepath.Join(procDir, "cmdline")
		cmdlineContent := strings.ReplaceAll(proc.cmdline, " ", "\x00")
		err = ioutil.WriteFile(cmdlineFile, []byte(cmdlineContent), 0644)
		require.NoError(m.t, err, "Failed to create cmdline file")
	}

	// Create status file with UID
	if !proc.accessErrors {
		statusContent := fmt.Sprintf("Name:   process-%d\nUid:    %s\n", proc.pid, proc.uid)
		statusFile := filepath.Join(procDir, "status")
		err = ioutil.WriteFile(statusFile, []byte(statusContent), 0644)
		require.NoError(m.t, err, "Failed to create status file")
	}

	// Create io file
	if !proc.accessErrors {
		ioContent := fmt.Sprintf("read_bytes: %.0f\nwrite_bytes: %.0f\n", proc.readBytes, proc.writeBytes)
		ioFile := filepath.Join(procDir, "io")
		err = ioutil.WriteFile(ioFile, []byte(ioContent), 0644)
		require.NoError(m.t, err, "Failed to create io file")
	}

	// Create exe symlink
	if !proc.accessErrors {
		exeLink := filepath.Join(procDir, "exe")
		// Create the target file first
		exeTarget := filepath.Join(m.rootDir, fmt.Sprintf("bin-process-%d", proc.pid))
		err = ioutil.WriteFile(exeTarget, []byte("#!/bin/sh\necho 'mock executable'\n"), 0755)
		require.NoError(m.t, err, "Failed to create exe target file")

		// Create the symlink
		err = os.Symlink(proc.execPath, exeLink)
		require.NoError(m.t, err, "Failed to create exe symlink")
	}
}

// GetRoot returns the path to the mock /proc root
func (m *mockProcFS) GetRoot() string {
	return m.rootDir
}

// Cleanup removes the temporary directory
func (m *mockProcFS) Cleanup() {
	m.cleanup()
}

// Mock process options for flexible configuration

type mockProcessOption func(*mockProcess)

func WithCmdline(cmdline string) mockProcessOption {
	return func(p *mockProcess) {
		p.cmdline = cmdline
	}
}

func WithUID(uid string) mockProcessOption {
	return func(p *mockProcess) {
		p.uid = uid
	}
}

func WithUsername(username string) mockProcessOption {
	return func(p *mockProcess) {
		p.username = username
	}
}

func WithExecutablePath(path string) mockProcessOption {
	return func(p *mockProcess) {
		p.execPath = path
	}
}

func WithIO(readBytes, writeBytes float64) mockProcessOption {
	return func(p *mockProcess) {
		p.readBytes = readBytes
		p.writeBytes = writeBytes
	}
}

func WithAccessErrors() mockProcessOption {
	return func(p *mockProcess) {
		p.accessErrors = true
	}
}

func WithNonExistent() mockProcessOption {
	return func(p *mockProcess) {
		p.shouldExist = false
	}
}