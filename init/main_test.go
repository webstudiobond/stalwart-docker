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
	"reflect"
	"sync"
	"testing"
	"time"
)

var errMockRead = errors.New("simulated filesystem permission error")

var errMockExec = errors.New("simulated execution error")

var errMockListen = errors.New("simulated tcp listen error")

var errMockDial = errors.New("simulated unix dial error")

var errMockClose = errors.New("simulated close error")

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

func mockFailingListenTCP(_ context.Context, _ string) (net.Listener, error) {
	return nil, errMockListen
}

func mockFailingDialUnix(_ context.Context, _ string) (net.Conn, error) {
	return nil, errMockDial
}

func mockSuccessfulStalwartCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
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
			if !tc.wantErr && !reflect.DeepEqual(gotEnv, tc.wantEnv) {
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
			if !reflect.DeepEqual(got, tc.want) {
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

			err := Run(context.Background(), []string{"--config", defaultConfigPath}, defaultSecretPath)
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
	originalGetDialer := getDialer
	defer func() {
		listenTCP = originalListen
		getDialer = originalGetDialer
	}()

	listenTCP = func(_ context.Context, _ string) (net.Listener, error) {
		return pl, nil
	}
	getDialer = func() contextDialer {
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
		}()

		b1, b2 := net.Pipe()
		defer closeCloser(b2)

		dialUnix = func(_ context.Context, _ string) (net.Conn, error) {
			return b1, nil
		}

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
	originalCreateStalwartCommand := createStalwartCommand
	defer func() {
		createStalwartCommand = originalCreateStalwartCommand
	}()

	t.Run("successful local process", func(t *testing.T) {
		createStalwartCommand = mockSuccessfulStalwartCommand
		if err := defaultExec(context.Background(), nil, []string{}); err != nil {
			t.Fatalf("defaultExec() error = %v", err)
		}
	})

	t.Run("missing Stalwart binary", func(t *testing.T) {
		createStalwartCommand = defaultCreateStalwartCommand
		if err := defaultExec(context.Background(), nil, nil); err == nil {
			t.Error("defaultExec() expected error on missing target binary, got nil")
		}
	})
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

func mockExitRecorder(code int) {
	recordedExitCode = code
}

var recordedExitCode = -1

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
