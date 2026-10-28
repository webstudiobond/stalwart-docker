package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	errMockRead   = errors.New("simulated filesystem permission error")
	errMockExec   = errors.New("simulated execution error")
	errMockListen = errors.New("simulated tcp listen error")
	errMockDial   = errors.New("simulated unix dial error")
	errMockClose  = errors.New("simulated close error")
	errMockOpen   = errors.New("simulated open error")
	errMockWrite  = errors.New("simulated write error")
	errMockScrub  = errors.New("simulated scrub error")
)

var (
	recordedExitCode     = -1
	mockProcDirectory    string
	mockDialAttemptCount int
	activePipeListener   net.Listener
	activeDialUnixConn   net.Conn
)

type failingCloser struct{}

func (failingCloser) Close() error {
	return errMockClose
}

type pipeDialer struct {
	conn net.Conn
}

func (d *pipeDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return d.conn, nil
}

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		conns: make(chan net.Conn, 1),
		done:  make(chan struct{}),
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.done)
	})
	return nil
}

func (*pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6379}
}

type mockSocketDialer struct {
	err  error
	conn net.Conn
}

func (m *mockSocketDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.conn, nil
}

var activeMockDialer = &mockSocketDialer{}

func mockCreateDialer() socketDialer {
	return activeMockDialer
}

type mockSuccessConn struct {
	net.Conn
}

func (m *mockSuccessConn) Close() error {
	return nil
}

func mockSuccessDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	return &mockSuccessConn{}, nil
}

func mockFailingDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	return nil, errMockDial
}

type mockErrorCloserConn struct {
	net.Conn
}

func (m *mockErrorCloserConn) Close() error {
	return errMockClose
}

func mockErrorCloserDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	return &mockErrorCloserConn{}, nil
}

func mockRetrySuccessDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	mockDialAttemptCount++
	if mockDialAttemptCount == 1 {
		return nil, errMockDial
	}
	return &mockSuccessConn{}, nil
}

func mockProcPath(pid int, entry string) string {
	if mockProcDirectory != "" {
		return filepath.Join(mockProcDirectory, strconv.Itoa(pid), entry)
	}
	return defaultProcPath(pid, entry)
}

func mockFailingOpenFile(_ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, errMockOpen
}

func mockPermissionOpenFile(_ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, os.ErrPermission
}

func mockPermissionReadFile(_ string) ([]byte, error) {
	return nil, os.ErrPermission
}

func mockFailingWriteMemory(_ *os.File, _ []byte, _ int64) (int, error) {
	return 0, errMockWrite
}

func mockPermissionWriteMemory(_ *os.File, _ []byte, _ int64) (int, error) {
	return 0, os.ErrPermission
}

func mockFailingCloseFile(_ *os.File) error {
	return errMockClose
}

func mockPermissionCloseFile(_ *os.File) error {
	return os.ErrPermission
}

func mockFailingScrub(_ context.Context, _ int) error {
	return errMockScrub
}

func mockExitRecorder(code int) {
	recordedExitCode = code
}

func mockSuccessfulRead(_ string) ([]byte, error) {
	return []byte("secret_token_123"), nil
}

func mockEmptyRead(_ string) ([]byte, error) {
	return []byte("   \n"), nil
}

func mockNotFoundRead(_ string) ([]byte, error) {
	return nil, fs.ErrNotExist
}

func mockFailingRead(_ string) ([]byte, error) {
	return nil, errMockRead
}

func mockSuccessfulExec(_ context.Context, _, _ []string) error {
	return nil
}

func mockFailingExec(_ context.Context, _, _ []string) error {
	return errMockExec
}

func mockEphemeralListenTCP(ctx context.Context, _ string) (net.Listener, error) {
	return defaultListenTCP(ctx, "127.0.0.1:0")
}

func mockPipeListenTCP(_ context.Context, _ string) (net.Listener, error) {
	return activePipeListener, nil
}

func mockFailingListenTCP(_ context.Context, _ string) (net.Listener, error) {
	return nil, errMockListen
}

func mockFailingDialUnix(_ context.Context, _ string) (net.Conn, error) {
	return nil, errMockDial
}

func mockConnDialUnix(_ context.Context, _ string) (net.Conn, error) {
	return activeDialUnixConn, nil
}

func mockNonExistentCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "/dev/null/non_existent_binary")
}

func mockTrueCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

func mockFalseCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "false")
}

func mockSeparateCredentials(path string) ([]byte, error) {
	switch path {
	case defaultRecoveryUserSecretPath:
		return []byte("bootstrap_user"), nil
	case defaultRecoveryPasswordSecretPath:
		return []byte("secret_token_123"), nil
	default:
		return nil, fs.ErrNotExist
	}
}

func mockMissingPassword(path string) ([]byte, error) {
	if path == defaultRecoveryUserSecretPath {
		return []byte("bootstrap_user"), nil
	}
	return nil, fs.ErrNotExist
}

func mockMissingUsername(path string) ([]byte, error) {
	if path == defaultRecoveryPasswordSecretPath {
		return []byte("secret_token_456"), nil
	}
	return nil, fs.ErrNotExist
}

func mockInvalidUsername(path string) ([]byte, error) {
	if path == defaultRecoveryUserSecretPath {
		return []byte("bootstrap:user"), nil
	}
	return []byte("secret_token_789"), nil
}

func mockEmptyUsername(path string) ([]byte, error) {
	if path == defaultRecoveryUserSecretPath {
		return []byte(" \n"), nil
	}
	return []byte("secret_token_987"), nil
}

func mockUsernameThenFailingPassword(path string) ([]byte, error) {
	if path == defaultRecoveryUserSecretPath {
		return []byte("bootstrap_user"), nil
	}
	return nil, errMockRead
}

func makeStatContent(comm, envStart, envEnd string, fieldCount int) string {
	var sb strings.Builder
	sb.WriteString("1234 (")
	sb.WriteString(comm)
	sb.WriteString(")")
	for i := range fieldCount {
		sb.WriteString(" ")
		switch i {
		case statEnvStartIndex:
			sb.WriteString(envStart)
		case statEnvEndIndex:
			sb.WriteString(envEnd)
		default:
			sb.WriteString("0")
		}
	}
	return sb.String()
}

func setupScrubChildFixture(t *testing.T) (pid int, statPath, memPath string) {
	t.Helper()
	tempDir := t.TempDir()
	pid = 4242
	pidDir := filepath.Join(tempDir, strconv.Itoa(pid))
	if err := os.Mkdir(pidDir, 0o750); err != nil {
		t.Fatalf("creating pid dir: %v", err)
	}

	statFilePath := filepath.Join(pidDir, "stat")
	memFilePath := filepath.Join(pidDir, "mem")

	validStatData := makeStatContent("stalwart", "10", "20", 49)
	if err := os.WriteFile(filepath.Clean(statFilePath), []byte(validStatData), 0o600); err != nil {
		t.Fatalf("writing stat file: %v", err)
	}

	initialMem := make([]byte, 30)
	for i := range initialMem {
		initialMem[i] = byte(i + 1)
	}
	if err := os.WriteFile(filepath.Clean(memFilePath), initialMem, 0o600); err != nil {
		t.Fatalf("writing mem file: %v", err)
	}

	mockProcDirectory = tempDir
	procPath = mockProcPath
	return pid, statFilePath, memFilePath
}

func setupScrubCleanup(t *testing.T) {
	t.Helper()
	origProcPath := procPath
	origReadFile := readFile
	origOpenFile := openFile
	origWriteMem := writeMemory
	origCloseFile := closeFile
	origDialTCP := dialTCP

	dialTCP = mockSuccessDialTCP

	t.Cleanup(func() {
		procPath = origProcPath
		readFile = origReadFile
		openFile = origOpenFile
		writeMemory = origWriteMem
		closeFile = origCloseFile
		dialTCP = origDialTCP
		mockProcDirectory = ""
	})
}

func TestPrepareEnvironment(t *testing.T) {
	baseEnv := []string{"PATH=/bin"}

	tests := []struct {
		readMock func(string) ([]byte, error)
		name     string
		wantEnv  []string
		wantErr  bool
	}{
		{
			readMock: mockNotFoundRead,
			name:     "missing secret files retain base environment without error",
			wantEnv:  baseEnv,
		},
		{
			readMock: mockEmptyRead,
			name:     "empty secret files retain base environment without error",
			wantEnv:  baseEnv,
		},
		{
			readMock: mockSeparateCredentials,
			name:     "separate username and password secrets create recovery credential",
			wantEnv:  []string{"PATH=/bin", "STALWART_RECOVERY_ADMIN=bootstrap_user:secret_token_123"},
		},
		{
			readMock: mockMissingPassword,
			name:     "missing password secret returns an error",
			wantErr:  true,
		},
		{
			readMock: mockMissingUsername,
			name:     "missing username secret returns an error",
			wantErr:  true,
		},
		{
			readMock: mockInvalidUsername,
			name:     "username containing credential separator returns an error",
			wantErr:  true,
		},
		{
			readMock: mockEmptyUsername,
			name:     "empty username with password returns an error",
			wantErr:  true,
		},
		{
			readMock: mockFailingRead,
			name:     "username filesystem error returns wrapped failure",
			wantErr:  true,
		},
		{
			readMock: mockUsernameThenFailingPassword,
			name:     "password filesystem error returns wrapped failure",
			wantErr:  true,
		},
	}

	originalReadFile := readFile
	defer func() {
		readFile = originalReadFile
	}()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readFile = tc.readMock
			gotEnv, err := PrepareEnvironment(baseEnv, defaultRecoveryUserSecretPath, defaultRecoveryPasswordSecretPath)
			if (err != nil) != tc.wantErr {
				t.Fatalf("PrepareEnvironment() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !slices.Equal(gotEnv, tc.wantEnv) {
				t.Errorf("PrepareEnvironment() = %v, want %v", gotEnv, tc.wantEnv)
			}
		})
	}
}

func TestBuildCommandArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "empty input args produces default configuration invocation",
			args: nil,
			want: []string{defaultBinaryPath, "--config", defaultConfigPath},
		},
		{
			name: "custom flags appends to binary path",
			args: []string{"--version"},
			want: []string{defaultBinaryPath, "--version"},
		},
		{
			name: "explicit binary argument preserves original arguments list",
			args: []string{defaultBinaryPath, "-v"},
			want: []string{defaultBinaryPath, "-v"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildCommandArguments(defaultBinaryPath, defaultConfigPath, tc.args)
			if !slices.Equal(got, tc.want) {
				t.Errorf("BuildCommandArguments() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		execMock   func(_ context.Context, _, _ []string) error
		listenMock func(context.Context, string) (net.Listener, error)
		readMock   func(string) ([]byte, error)
		name       string
		wantErr    bool
	}{
		{
			execMock:   mockSuccessfulExec,
			listenMock: mockEphemeralListenTCP,
			readMock:   mockSuccessfulRead,
			name:       "successful preparation, proxy startup, and execution transfer",
			wantErr:    false,
		},
		{
			execMock:   mockSuccessfulExec,
			listenMock: mockEphemeralListenTCP,
			readMock:   mockFailingRead,
			name:       "failure during environment preparation propagates error",
			wantErr:    true,
		},
		{
			execMock:   mockSuccessfulExec,
			listenMock: mockFailingListenTCP,
			readMock:   mockSuccessfulRead,
			name:       "failure during socket proxy startup propagates error",
			wantErr:    true,
		},
		{
			execMock:   mockFailingExec,
			listenMock: mockEphemeralListenTCP,
			readMock:   mockSuccessfulRead,
			name:       "failure during process execution propagates error",
			wantErr:    true,
		},
	}

	originalReadFile := readFile
	originalExec := execProcess
	originalListen := listenTCP
	defer func() {
		readFile = originalReadFile
		execProcess = originalExec
		listenTCP = originalListen
	}()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readFile = tc.readMock
			execProcess = tc.execMock
			listenTCP = tc.listenMock

			err := Run(context.Background(), []string{"--dry-run"}, defaultSecretPath)
			if (err != nil) != tc.wantErr {
				t.Errorf("Run() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestStartSocketProxy(t *testing.T) {
	pl := newPipeListener()
	clientConn, proxyServerConn := net.Pipe()
	defer closeCloser(clientConn)

	backendClientConn, backendServerConn := net.Pipe()
	defer closeCloser(backendServerConn)

	originalListen := listenTCP
	originalCreateDialer := createDialer
	defer func() {
		listenTCP = originalListen
		createDialer = originalCreateDialer
		activePipeListener = nil
	}()

	activePipeListener = pl
	listenTCP = mockPipeListenTCP
	createDialer = func() socketDialer {
		return &pipeDialer{conn: backendClientConn}
	}

	go func() {
		defer closeCloser(backendServerConn)
		buf := make([]byte, 4)
		if _, readErr := io.ReadFull(backendServerConn, buf); readErr != nil {
			return
		}
		if _, writeErr := backendServerConn.Write([]byte("PONG")); writeErr != nil {
			return
		}
	}()

	proxyCloser, err := StartSocketProxy(context.Background(), defaultProxyListenAddr, defaultValkeySocketPath)
	if err != nil {
		t.Fatalf("StartSocketProxy() error = %v", err)
	}
	defer closeCloser(proxyCloser)

	pl.conns <- proxyServerConn

	if _, err := clientConn.Write([]byte("PING")); err != nil {
		t.Fatalf("write to proxy: %v", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("read from proxy: %v", err)
	}

	if !bytes.Equal(reply, []byte("PONG")) {
		t.Errorf("proxy reply = %q, want %q", reply, "PONG")
	}
}

func TestHandleProxyConnection_EdgeCases(t *testing.T) {
	closeCloser(failingCloser{})

	if _, err := defaultListenTCP(context.Background(), "127.0.0.1:-1"); err == nil {
		t.Error("defaultListenTCP() expected error on invalid port, got nil")
	}

	if _, err := defaultDialUnix(context.Background(), "/nonexistent/valkey.sock"); err == nil {
		t.Error("defaultDialUnix() expected error on missing socket, got nil")
	}

	t.Run("dial failure closes client connection", func(_ *testing.T) {
		originalDial := dialUnix
		defer func() {
			dialUnix = originalDial
		}()
		dialUnix = mockFailingDialUnix

		c1, c2 := net.Pipe()
		defer closeCloser(c2)

		handleProxyConnection(context.Background(), c1, "/nonexistent/sock")
	})

	t.Run("context cancellation closes active relay", func(_ *testing.T) {
		originalDial := dialUnix
		defer func() {
			dialUnix = originalDial
			activeDialUnixConn = nil
		}()

		b1, b2 := net.Pipe()
		defer closeCloser(b2)

		activeDialUnixConn = b1
		dialUnix = mockConnDialUnix

		c1, c2 := net.Pipe()
		defer closeCloser(c2)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		handleProxyConnection(ctx, c1, "ignored")
	})
}

func TestDefaultExec(t *testing.T) {
	ctx := context.Background()
	origCreate := createCommand
	origScrub := scrubChildMemory
	t.Cleanup(func() {
		createCommand = origCreate
		scrubChildMemory = origScrub
	})

	createCommand = mockNonExistentCommand
	if err := defaultExec(ctx, nil, nil); err == nil {
		t.Fatal("expected error executing non-existent binary, got nil")
	}

	createCommand = mockFalseCommand
	if err := defaultExec(ctx, nil, nil); err == nil {
		t.Fatal("expected error when command exits with failure, got nil")
	}

	createCommand = mockTrueCommand
	scrubChildMemory = mockFailingScrub
	if err := defaultExec(ctx, nil, nil); err != nil {
		t.Fatalf("unexpected error executing true binary with failing scrub: %v", err)
	}

	scrubChildMemory = origScrub
	if err := defaultExec(ctx, nil, nil); err != nil {
		t.Fatalf("unexpected error executing true binary: %v", err)
	}
}

func TestDefaultCreateCommand(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantArgs []string
	}{
		{
			name:     "empty_args",
			args:     nil,
			wantPath: defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath},
		},
		{
			name:     "binary_only",
			args:     []string{defaultBinaryPath},
			wantPath: defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath},
		},
		{
			name:     "with_arguments",
			args:     []string{defaultBinaryPath, "--verbose"},
			wantPath: defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath, "--verbose"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := defaultCreateCommand(ctx, tt.args)
			if cmd == nil {
				t.Fatal("expected command object, got nil")
			}
			if cmd.Path != tt.wantPath {
				t.Fatalf("expected command path %q, got %q", tt.wantPath, cmd.Path)
			}
			if !slices.Equal(cmd.Args, tt.wantArgs) {
				t.Fatalf("expected args %v, got %v", tt.wantArgs, cmd.Args)
			}
			if cmd.Cancel == nil {
				t.Fatal("expected cancel function, got nil")
			}
			if cmd.WaitDelay != 5*time.Second {
				t.Fatalf("expected wait delay 5s, got %v", cmd.WaitDelay)
			}
			if err := cmd.Cancel(); err != nil {
				t.Fatalf("unexpected cancel error: %v", err)
			}
		})
	}

	runningCmd := exec.CommandContext(ctx, "sleep", "10")
	cancelFn := cancelCommand(runningCmd)
	if err := runningCmd.Start(); err != nil {
		t.Fatalf("cmd.Start() unexpected error: %v", err)
	}
	defer func() {
		if waitErr := runningCmd.Wait(); waitErr != nil {
			if _, ok := errors.AsType[*exec.ExitError](waitErr); !ok {
				t.Errorf("runningCmd.Wait() error: %v", waitErr)
			}
		}
	}()
	if err := cancelFn(); err != nil {
		t.Fatalf("cancelFn() unexpected error: %v", err)
	}
}

func TestDefaultReadFile(t *testing.T) {
	tempDir := t.TempDir()
	secretPath := filepath.Join(tempDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret_token_123"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	data, err := defaultReadFile(secretPath)
	if err != nil {
		t.Fatalf("defaultReadFile() error = %v", err)
	}
	if string(data) != "secret_token_123" {
		t.Errorf("defaultReadFile() = %q, want %q", data, "secret_token_123")
	}

	if _, err := defaultReadFile(filepath.Join(tempDir, "missing-secret.txt")); err == nil {
		t.Error("defaultReadFile() expected error for nonexistent file, got nil")
	}
}

func TestDefaultCloseFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "file.txt")
	file, err := os.Create(filepath.Clean(filePath))
	if err != nil {
		t.Fatalf("creating test file: %v", err)
	}

	if err := defaultCloseFile(file); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if err := defaultCloseFile(file); err == nil {
		t.Fatal("expected error closing already closed file, got nil")
	}
}

func TestDefaultWriteMemory(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "mem.bin")
	cleanPath := filepath.Clean(filePath)
	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("creating test file: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing file: %v", closeErr)
		}
	}()

	n, err := defaultWriteMemory(file, []byte("payload"), 0)
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if n != 7 {
		t.Fatalf("expected 7 bytes written, got %d", n)
	}

	readOnlyFile, err := os.OpenFile(cleanPath, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("opening read-only file: %v", err)
	}
	defer func() {
		if closeErr := readOnlyFile.Close(); closeErr != nil {
			t.Errorf("closing read-only file: %v", closeErr)
		}
	}()

	if _, err := defaultWriteMemory(readOnlyFile, []byte("data"), 0); err == nil {
		t.Fatal("expected write error on read-only file, got nil")
	}
}

func TestDefaultOpenFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "open.txt")
	cleanPath := filepath.Clean(filePath)
	if err := os.WriteFile(cleanPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	file, err := defaultOpenFile(cleanPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("unexpected open error: %v", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("closing file: %v", closeErr)
	}

	if _, err := defaultOpenFile(filepath.Join(tempDir, "missing.txt"), os.O_RDONLY, 0); err == nil {
		t.Fatal("expected error opening missing file, got nil")
	}
}

func TestDefaultCreateDialer(t *testing.T) {
	d := defaultCreateDialer()
	if d == nil {
		t.Fatal("expected non-nil dialer, got nil")
	}
}

func TestDefaultDialTCP(t *testing.T) {
	origDialer := createDialer
	createDialer = mockCreateDialer
	t.Cleanup(func() { createDialer = origDialer })

	activeMockDialer.err = errMockDial
	activeMockDialer.conn = nil
	if _, err := defaultDialTCP(context.Background(), "tcp", "127.0.0.1:8080"); err == nil {
		t.Fatal("expected dial error, got nil")
	}

	activeMockDialer.err = nil
	activeMockDialer.conn = &mockSuccessConn{}
	conn, err := defaultDialTCP(context.Background(), "tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil conn, got nil")
	}
}

func TestDefaultProcPath(t *testing.T) {
	got := defaultProcPath(42, "stat")
	expected := filepath.Join(defaultProcDir, "42", "stat")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name         string
		statContent  string
		wantEnvStart int64
		wantEnvEnd   int64
		expectErr    bool
	}{
		{
			name:        "missing_closing_comm_paren",
			statContent: "1234 stalwart 0 0 0",
			expectErr:   true,
		},
		{
			name:        "insufficient_fields_after_comm",
			statContent: makeStatContent("stalwart", "100", "200", 20),
			expectErr:   true,
		},
		{
			name:        "invalid_env_start_non_number",
			statContent: makeStatContent("stalwart", "not_a_number", "200", 49),
			expectErr:   true,
		},
		{
			name:        "invalid_env_end_non_number",
			statContent: makeStatContent("stalwart", "100", "not_a_number", 49),
			expectErr:   true,
		},
		{
			name:        "negative_or_zero_env_start",
			statContent: makeStatContent("stalwart", "0", "200", 49),
			expectErr:   true,
		},
		{
			name:        "env_start_greater_than_env_end",
			statContent: makeStatContent("stalwart", "300", "200", 49),
			expectErr:   true,
		},
		{
			name:        "env_range_exceeds_max_limit",
			statContent: makeStatContent("stalwart", "1000", "34554433", 49),
			expectErr:   true,
		},
		{
			name:         "valid_stat_with_nested_comm",
			statContent:  makeStatContent("stalwart (worker) proc", "1000", "2000", 49),
			wantEnvStart: 1000,
			wantEnvEnd:   2000,
			expectErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envStart, envEnd, err := parseProcStat(tt.statContent)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for case %q, got nil", tt.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for case %q: %v", tt.name, err)
			}
			if envStart != tt.wantEnvStart || envEnd != tt.wantEnvEnd {
				t.Fatalf("expected (%d, %d), got (%d, %d)", tt.wantEnvStart, tt.wantEnvEnd, envStart, envEnd)
			}
		})
	}
}

func TestWaitForChildReadiness(t *testing.T) {
	origDial := dialTCP
	t.Cleanup(func() { dialTCP = origDial })

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForChildReadiness(canceledCtx, "127.0.0.1:8080", time.Second); err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	dialTCP = mockSuccessDialTCP
	if err := waitForChildReadiness(context.Background(), "127.0.0.1:8080", time.Second); err != nil {
		t.Fatalf("unexpected error on ready socket: %v", err)
	}

	dialTCP = mockErrorCloserDialTCP
	if err := waitForChildReadiness(context.Background(), "127.0.0.1:8080", time.Second); err == nil {
		t.Fatal("expected error when socket close fails, got nil")
	}

	dialTCP = mockFailingDialTCP
	if err := waitForChildReadiness(context.Background(), "127.0.0.1:8080", -1*time.Second); err != nil {
		t.Fatalf("expected nil when deadline passed, got %v", err)
	}

	ctxWait, cancelWait := context.WithCancel(context.Background())
	time.AfterFunc(2*time.Millisecond, cancelWait)
	if err := waitForChildReadiness(ctxWait, "127.0.0.1:8080", 2*time.Second); err == nil {
		t.Fatal("expected error when context canceled during wait, got nil")
	}

	mockDialAttemptCount = 0
	dialTCP = mockRetrySuccessDialTCP
	if err := waitForChildReadiness(context.Background(), "127.0.0.1:8080", time.Second); err != nil {
		t.Fatalf("unexpected error on retry dial: %v", err)
	}
}

func TestDefaultScrubChildMemory_ReadinessErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, _ := setupScrubChildFixture(t)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := defaultScrubChildMemory(canceledCtx, pid); err != nil {
		t.Fatalf("expected nil when readiness context canceled, got %v", err)
	}

	dialTCP = mockErrorCloserDialTCP
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on readiness failure, got nil")
	}
}

func TestDefaultScrubChildMemory_ReadAndParseErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, statFilePath, _ := setupScrubChildFixture(t)

	if err := defaultScrubChildMemory(context.Background(), 9999); err != nil {
		t.Fatalf("expected nil when stat file missing, got %v", err)
	}

	readFile = mockPermissionReadFile
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on stat read permission error, got %v", err)
	}

	readFile = mockFailingRead
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic stat read error, got nil")
	}
	readFile = defaultReadFile

	if err := os.WriteFile(filepath.Clean(statFilePath), []byte("corrupt stat data"), 0o600); err != nil {
		t.Fatalf("overwriting stat: %v", err)
	}
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on corrupt stat data, got nil")
	}
}

func TestDefaultScrubChildMemory_OpenErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, memFilePath := setupScrubChildFixture(t)

	if err := os.Remove(filepath.Clean(memFilePath)); err != nil {
		t.Fatalf("removing mem file: %v", err)
	}
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil when mem file missing, got %v", err)
	}

	openFile = mockPermissionOpenFile
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on mem open permission error, got %v", err)
	}

	openFile = mockFailingOpenFile
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic mem open error, got nil")
	}
}

func TestDefaultScrubChildMemory_WriteErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, _ := setupScrubChildFixture(t)

	writeMemory = mockPermissionWriteMemory
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on mem write permission error, got %v", err)
	}

	writeMemory = mockFailingWriteMemory
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic mem write error, got nil")
	}
}

func TestDefaultScrubChildMemory_CloseErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, _ := setupScrubChildFixture(t)

	closeFile = mockPermissionCloseFile
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on mem close permission error, got %v", err)
	}

	closeFile = mockFailingCloseFile
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic mem close error, got nil")
	}
}

func TestDefaultScrubChildMemory_Success(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, memFilePath := setupScrubChildFixture(t)

	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("unexpected error on successful scrub: %v", err)
	}

	scrubbedData, err := os.ReadFile(filepath.Clean(memFilePath))
	if err != nil {
		t.Fatalf("reading scrubbed mem file: %v", err)
	}
	for i := range 10 {
		if scrubbedData[i] != byte(i+1) {
			t.Fatalf("expected untouched byte at %d, got %d", i, scrubbedData[i])
		}
	}
	for i := 10; i < 20; i++ {
		if scrubbedData[i] != 0 {
			t.Fatalf("expected zeroed byte at %d, got %d", i, scrubbedData[i])
		}
	}
	for i := 20; i < 30; i++ {
		if scrubbedData[i] != byte(i+1) {
			t.Fatalf("expected untouched byte at %d, got %d", i, scrubbedData[i])
		}
	}
}

func TestMain_Success(t *testing.T) {
	originalReadFile := readFile
	originalExec := execProcess
	originalListen := listenTCP
	originalExit := exitFunc
	defer func() {
		readFile = originalReadFile
		execProcess = originalExec
		listenTCP = originalListen
		exitFunc = originalExit
	}()

	readFile = mockNotFoundRead
	execProcess = mockSuccessfulExec
	listenTCP = mockEphemeralListenTCP
	recordedExitCode = 0
	exitFunc = mockExitRecorder

	main()

	if recordedExitCode != 0 {
		t.Errorf("main() expected clean exit, recorded = %d", recordedExitCode)
	}
}

func TestMain_Failure(t *testing.T) {
	originalReadFile := readFile
	originalExit := exitFunc
	defer func() {
		readFile = originalReadFile
		exitFunc = originalExit
	}()

	readFile = mockFailingRead
	recordedExitCode = -1
	exitFunc = mockExitRecorder

	main()

	if recordedExitCode != 1 {
		t.Errorf("main() expected fatal exit code 1, recorded = %d", recordedExitCode)
	}
}
