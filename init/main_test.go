package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

var errMockRead = errors.New("simulated filesystem permission error")

var errMockExec = errors.New("simulated execution error")

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
		readMock func(string) ([]byte, error)
		execMock func(_ context.Context, _, _ []string) error
		name     string
		wantErr  bool
	}{
		{
			readMock: mockSuccessfulRead,
			execMock: mockSuccessfulExec,
			name:     "successful preparation and execution transfer",
			wantErr:  false,
		},
		{
			readMock: mockFailingRead,
			execMock: mockSuccessfulExec,
			name:     "failure during environment preparation propagates error",
			wantErr:  true,
		},
		{
			readMock: mockSuccessfulRead,
			execMock: mockFailingExec,
			name:     "failure during process execution propagates error",
			wantErr:  true,
		},
	}

	originalReadFile := readFile
	originalExec := execProcess
	defer func() {
		readFile = originalReadFile
		execProcess = originalExec
	}()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readFile = tc.readMock
			execProcess = tc.execMock

			err := Run(context.Background(), []string{"--config", defaultConfigPath}, defaultSecretPath)
			if (err != nil) != tc.wantErr {
				t.Errorf("Run() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
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
	originalExit := exitFunc
	defer func() {
		readFile = originalReadFile
		execProcess = originalExec
		exitFunc = originalExit
	}()

	readFile = mockNotFoundRead
	execProcess = mockSuccessfulExec
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
