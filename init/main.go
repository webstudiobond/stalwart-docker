// Package main implements a minimal static initialization binary that mediates
// startup credentials before transferring execution to the Stalwart mail server daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultRecoveryUserSecretPath = "/run/secrets/recovery_user"

const defaultRecoveryPasswordSecretPath = "/run/secrets/recovery_password"

const defaultSecretPath = defaultRecoveryPasswordSecretPath

const defaultBinaryPath = "/usr/local/bin/stalwart"

const defaultConfigPath = "/etc/stalwart/config.json"

const adminEnvKey = "STALWART_RECOVERY_ADMIN"

func defaultReadFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading secret file: %w", err)
	}
	return data, nil
}

func defaultCreateStalwartCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, defaultBinaryPath, "--config", defaultConfigPath)
}

func defaultExec(ctx context.Context, _, env []string) error {
	cmd := createStalwartCommand(ctx)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running target binary: %w", err)
	}
	return nil
}

var (
	createStalwartCommand = defaultCreateStalwartCommand
	readFile              = defaultReadFile
	execProcess           = defaultExec
	exitFunc              = os.Exit
)

func readOptionalSecret(path string) (secret string, exists bool, err error) {
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading secret %q: %w", path, err)
	}

	return strings.TrimSpace(string(data)), true, nil
}

// PrepareEnvironment reads separate recovery username and password secrets so the
// recovery account is not tied to a predictable administrator name.
func PrepareEnvironment(baseEnv []string, usernameSecretPath, passwordSecretPath string) ([]string, error) {
	username, usernameExists, err := readOptionalSecret(usernameSecretPath)
	if err != nil {
		return nil, fmt.Errorf("loading recovery username: %w", err)
	}

	password, passwordExists, err := readOptionalSecret(passwordSecretPath)
	if err != nil {
		return nil, fmt.Errorf("loading recovery password: %w", err)
	}

	if !usernameExists && !passwordExists {
		return baseEnv, nil
	}
	if !usernameExists || !passwordExists {
		return nil, errors.New("recovery username and password secrets must be provided together")
	}
	if username == "" && password == "" {
		return baseEnv, nil
	}
	if username == "" || password == "" {
		return nil, errors.New("recovery username and password secrets must not be empty")
	}
	if strings.ContainsAny(username, ":\r\n") {
		return nil, errors.New("recovery username must not contain colon or newline characters")
	}

	env := make([]string, 0, len(baseEnv)+1)
	env = append(env, baseEnv...)
	env = append(env, fmt.Sprintf("%s=%s:%s", adminEnvKey, username, password))

	return env, nil
}

// BuildCommandArguments constructs the execution argument vector, ensuring
// the default configuration path is supplied when custom flags are omitted.
func BuildCommandArguments(binaryPath, configPath string, args []string) []string {
	if len(args) == 0 {
		return []string{binaryPath, "--config", configPath}
	}

	if args[0] == binaryPath {
		res := make([]string, len(args))
		copy(res, args)
		return res
	}

	res := make([]string, 0, len(args)+1)
	res = append(res, binaryPath)
	res = append(res, args...)
	return res
}

// Run executes the initialization workflow by loading credentials and transferring execution.
func Run(ctx context.Context, args []string, secretPath string) error {
	env, err := PrepareEnvironment(os.Environ(), defaultRecoveryUserSecretPath, secretPath)
	if err != nil {
		return fmt.Errorf("preparing runtime environment: %w", err)
	}

	argv := BuildCommandArguments(defaultBinaryPath, defaultConfigPath, args)
	if execErr := execProcess(ctx, argv, env); execErr != nil {
		return fmt.Errorf("transferring execution to stalwart: %w", execErr)
	}

	return nil
}

func main() {
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}

	ctx := context.Background()
	if err := Run(ctx, args, defaultSecretPath); err != nil {
		fmt.Fprintf(os.Stderr, "init fatal: %v\n", err)
		exitFunc(1)
	}
}
