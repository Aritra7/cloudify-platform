package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const maxOutputLineBytes = 1 << 20

// Stream identifies the child-process output channel.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Command is an argument-based process invocation. It intentionally has no
// shell command string.
type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

// OutputFunc receives one bounded line of process output.
type OutputFunc func(Stream, string) error

// CommandRunner executes a command and supervises its process tree.
type CommandRunner interface {
	Run(context.Context, Command, OutputFunc) error
}

// OSCommandRunner executes processes in their own group so cancellation also
// reaches descendants such as Docker, gcloud, and build tools.
type OSCommandRunner struct {
	TerminationGracePeriod time.Duration
}

// Run executes one command without invoking a shell.
func (runner OSCommandRunner) Run(ctx context.Context, command Command, output OutputFunc) error {
	if command.Name == "" {
		return errors.New("command name is required")
	}
	gracePeriod := runner.TerminationGracePeriod
	if gracePeriod <= 0 {
		gracePeriod = 10 * time.Second
	}

	process := exec.Command(command.Name, command.Args...)
	process.Dir = command.Dir
	process.Env = command.Env
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, stdoutWriter := io.Pipe()
	stderr, stderrWriter := io.Pipe()
	process.Stdout = stdoutWriter
	process.Stderr = stderrWriter
	if err := process.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return fmt.Errorf("start %s: %w", command.Name, err)
	}

	outputContext, cancelOutput := context.WithCancel(ctx)
	defer cancelOutput()
	var outputErr error
	var outputErrMu sync.Mutex
	var readers sync.WaitGroup
	readers.Add(2)
	read := func(stream Stream, reader io.Reader) {
		defer readers.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), maxOutputLineBytes)
		for scanner.Scan() {
			if output == nil {
				continue
			}
			if err := output(stream, scanner.Text()); err != nil {
				outputErrMu.Lock()
				outputErr = errors.Join(outputErr, err)
				outputErrMu.Unlock()
				if pipe, ok := reader.(*io.PipeReader); ok {
					_ = pipe.CloseWithError(err)
				}
				cancelOutput()
				return
			}
		}
		if err := scanner.Err(); err != nil {
			outputErrMu.Lock()
			outputErr = errors.Join(outputErr, err)
			outputErrMu.Unlock()
			if pipe, ok := reader.(*io.PipeReader); ok {
				_ = pipe.CloseWithError(err)
			}
			cancelOutput()
		}
	}
	go read(StreamStdout, stdout)
	go read(StreamStderr, stderr)

	waitResult := make(chan error, 1)
	go func() {
		waitErr := process.Wait()
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		waitResult <- waitErr
	}()

	var processErr error
	select {
	case processErr = <-waitResult:
	case <-outputContext.Done():
		_ = syscall.Kill(-process.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(gracePeriod)
		select {
		case processErr = <-waitResult:
			timer.Stop()
		case <-timer.C:
			_ = syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
			processErr = <-waitResult
		}
	}
	readers.Wait()

	outputErrMu.Lock()
	defer outputErrMu.Unlock()
	if outputErr != nil {
		return fmt.Errorf("capture %s output: %w", command.Name, outputErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if processErr != nil {
		return fmt.Errorf("run %s: %w", command.Name, processErr)
	}
	return nil
}
