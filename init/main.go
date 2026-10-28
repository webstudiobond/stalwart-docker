// Package main implements a minimal static initialization binary that mediates
// startup credentials, enforces process memory sanitization, and bridges loopback
// Valkey TCP connections to a UNIX domain socket before transferring execution to Stalwart.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultRecoveryUserSecretPath     = "/run/secrets/stalwart_recovery_user"
	defaultRecoveryPasswordSecretPath = "/run/secrets/stalwart_recovery_password"
	defaultSecretPath                 = defaultRecoveryPasswordSecretPath
	defaultBinaryPath                 = "/usr/local/bin/stalwart"
	defaultConfigPath                 = "/etc/stalwart/config.json"
	defaultProxyListenAddr            = "127.0.0.1:6379"
	defaultValkeySocketPath           = "/var/run/valkey/valkey.sock"
	adminEnvKey                       = "STALWART_RECOVERY_ADMIN"
)

const (
	defaultLoopbackAddress  = "127.0.0.1"
	defaultStalwartPort     = "8080"
	defaultProcDir          = "/proc"
	defaultReadinessTimeout = 30 * time.Second
	childReadinessInterval  = 50 * time.Millisecond
	maxEnvRangeSize         = 32 * 1024 * 1024
	minStatFieldsAfterComm  = 49
	statEnvStartIndex       = 47
	statEnvEndIndex         = 48
)

func defaultReadFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading secret file: %w", err)
	}
	return data, nil
}

func defaultCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing file: %w", err)
	}
	return nil
}

func defaultWriteMemory(f *os.File, b []byte, off int64) (int, error) {
	n, err := f.WriteAt(b, off)
	if err != nil {
		return n, fmt.Errorf("writing memory: %w", err)
	}
	return n, nil
}

func defaultOpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	cleanPath := filepath.Clean(name)
	file, err := os.OpenFile(cleanPath, flag, perm)
	if err != nil {
		return nil, fmt.Errorf("opening file %q: %w", cleanPath, err)
	}
	return file, nil
}

type socketDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func defaultCreateDialer() socketDialer {
	return &net.Dialer{}
}

func defaultDialTCP(ctx context.Context, network, address string) (net.Conn, error) {
	d := createDialer()
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("dialing %s %s: %w", network, address, err)
	}
	return conn, nil
}

func defaultProcPath(pid int, entry string) string {
	return filepath.Join(defaultProcDir, strconv.Itoa(pid), entry)
}

func cancelCommand(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
}

func defaultCreateCommand(ctx context.Context, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, defaultBinaryPath)
	if len(args) > 1 {
		cmd.Args = append([]string{defaultBinaryPath}, args[1:]...)
	}
	cmd.Cancel = cancelCommand(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func parseProcStat(statContent string) (envStart, envEnd int64, err error) {
	_, remainder, found := strings.CutLast(statContent, ")")
	if !found {
		return 0, 0, errors.New("missing closing parenthesis for comm in stat content")
	}

	fields := strings.Fields(remainder)
	if len(fields) < minStatFieldsAfterComm {
		return 0, 0, fmt.Errorf("stat fields count %d below required minimum %d", len(fields), minStatFieldsAfterComm)
	}

	envStart, err = strconv.ParseInt(fields[statEnvStartIndex], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing env_start from stat: %w", err)
	}

	envEnd, err = strconv.ParseInt(fields[statEnvEndIndex], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing env_end from stat: %w", err)
	}

	if envStart <= 0 || envEnd <= envStart {
		return 0, 0, fmt.Errorf("invalid environment boundaries: env_start (%d) >= env_end (%d)", envStart, envEnd)
	}

	if envEnd-envStart > maxEnvRangeSize {
		return 0, 0, fmt.Errorf("environment range size %d exceeds limit of %d bytes", envEnd-envStart, maxEnvRangeSize)
	}

	return envStart, envEnd, nil
}

func waitForChildReadiness(ctx context.Context, addr string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("readiness context canceled: %w", err)
		}
		conn, err := dialTCP(waitCtx, "tcp", addr)
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				return fmt.Errorf("closing readiness socket: %w", closeErr)
			}
			return nil
		}
		timer := time.NewTimer(childReadinessInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("readiness context canceled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func isIgnorableMemoryError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

func defaultScrubChildMemory(ctx context.Context, pid int) (err error) {
	targetAddr := net.JoinHostPort(defaultLoopbackAddress, defaultStalwartPort)
	if readinessErr := waitForChildReadiness(ctx, targetAddr, defaultReadinessTimeout); readinessErr != nil {
		if isIgnorableMemoryError(readinessErr) || errors.Is(readinessErr, context.Canceled) {
			return nil
		}
		return fmt.Errorf("synchronizing child readiness: %w", readinessErr)
	}

	statPath := procPath(pid, "stat")
	statBytes, readErr := readFile(statPath)
	if readErr != nil {
		if isIgnorableMemoryError(readErr) {
			return nil
		}
		return fmt.Errorf("reading stat file %q: %w", statPath, readErr)
	}

	envStart, envEnd, parseErr := parseProcStat(string(statBytes))
	if parseErr != nil {
		return fmt.Errorf("parsing stat file %q: %w", statPath, parseErr)
	}

	memPath := procPath(pid, "mem")
	memFile, openErr := openFile(memPath, os.O_RDWR, 0)
	if openErr != nil {
		if isIgnorableMemoryError(openErr) {
			return nil
		}
		return fmt.Errorf("opening mem file %q: %w", memPath, openErr)
	}
	defer func() {
		if closeErr := closeFile(memFile); closeErr != nil && err == nil && !isIgnorableMemoryError(closeErr) {
			err = fmt.Errorf("closing mem file %q: %w", memPath, closeErr)
		}
	}()

	zeros := make([]byte, envEnd-envStart)
	if _, writeErr := writeMemory(memFile, zeros, envStart); writeErr != nil {
		if isIgnorableMemoryError(writeErr) {
			return nil
		}
		return fmt.Errorf("zeroing memory in %q: %w", memPath, writeErr)
	}

	return nil
}

func defaultExec(ctx context.Context, args, env []string) error {
	cmd := createCommand(ctx, args)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting target binary: %w", err)
	}

	pid := cmd.Process.Pid
	scrubCtx, cancelScrub := context.WithCancel(ctx)
	defer cancelScrub()

	scrubDone := make(chan struct{})
	go func() {
		defer close(scrubDone)
		if scrubErr := scrubChildMemory(scrubCtx, pid); scrubErr != nil {
			fmt.Fprintf(os.Stderr, "memory scrubbing failed: %v\n", scrubErr)
		}
	}()

	cmd.Env = nil
	clear(env)
	runtime.GC()

	waitErr := cmd.Wait()
	cancelScrub()
	<-scrubDone

	if waitErr != nil {
		return fmt.Errorf("running target binary: %w", waitErr)
	}
	return nil
}

func defaultListenTCP(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %q: %w", addr, err)
	}
	return ln, nil
}

func defaultDialUnix(ctx context.Context, socketPath string) (net.Conn, error) {
	conn, err := createDialer().DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dialing unix socket %q: %w", socketPath, err)
	}
	return conn, nil
}

var (
	closeFile        = defaultCloseFile
	createCommand    = defaultCreateCommand
	createDialer     = defaultCreateDialer
	dialTCP          = defaultDialTCP
	dialUnix         = defaultDialUnix
	execProcess      = defaultExec
	exitFunc         = os.Exit
	listenTCP        = defaultListenTCP
	openFile         = defaultOpenFile
	procPath         = defaultProcPath
	readFile         = defaultReadFile
	scrubChildMemory = defaultScrubChildMemory
	writeMemory      = defaultWriteMemory
)

func closeCloser(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}

func pipeConn(dst io.Writer, src io.Reader, done chan<- struct{}) {
	if _, err := io.Copy(dst, src); err != nil {
		done <- struct{}{}
		return
	}
	done <- struct{}{}
}

func handleProxyConnection(ctx context.Context, clientConn net.Conn, socketPath string) {
	backendConn, err := dialUnix(ctx, socketPath)
	if err != nil {
		closeCloser(clientConn)
		return
	}

	done := make(chan struct{}, 2)
	go pipeConn(backendConn, clientConn, done)
	go pipeConn(clientConn, backendConn, done)

	select {
	case <-ctx.Done():
		closeCloser(clientConn)
		closeCloser(backendConn)
		<-done
		<-done
	case <-done:
		closeCloser(clientConn)
		closeCloser(backendConn)
		<-done
	}
}

func acceptProxyConnections(ctx context.Context, ln net.Listener, socketPath string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleProxyConnection(ctx, conn, socketPath)
	}
}

// StartSocketProxy launches a loopback TCP listener that relays connections to a
// UNIX domain socket so services lacking native UNIX socket support can communicate
// over isolated tmpfs sockets.
func StartSocketProxy(ctx context.Context, listenAddr, socketPath string) (io.Closer, error) {
	ln, err := listenTCP(ctx, listenAddr)
	if err != nil {
		return nil, fmt.Errorf("starting socket proxy listener: %w", err)
	}

	go acceptProxyConnections(ctx, ln, socketPath)

	return ln, nil
}

func readOptionalSecret(path string) (secret string, exists bool, err error) {
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading secret %q: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(data))
	clear(data)
	return trimmed, true, nil
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

// Run executes the initialization workflow by loading credentials, starting the
// Valkey UNIX socket proxy, and transferring execution to the server binary.
func Run(ctx context.Context, args []string, secretPath string) error {
	env, err := PrepareEnvironment(os.Environ(), defaultRecoveryUserSecretPath, secretPath)
	if err != nil {
		return fmt.Errorf("preparing runtime environment: %w", err)
	}

	proxyCloser, err := StartSocketProxy(ctx, defaultProxyListenAddr, defaultValkeySocketPath)
	if err != nil {
		return fmt.Errorf("initializing valkey socket proxy: %w", err)
	}
	defer closeCloser(proxyCloser)

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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, args, defaultSecretPath); err != nil {
		fmt.Fprintf(os.Stderr, "init fatal: %v\n", err)
		exitFunc(1)
	}
}
