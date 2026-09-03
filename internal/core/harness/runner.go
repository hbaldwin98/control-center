package harness

import (
	"io"
	"os"
	"os/exec"
)

// Command is the fully resolved process invocation. Only configured profiles create it.
type Command struct {
	Path string
	Args []string
	Dir  string
}

// Process is the small process boundary the service needs to supervise.
type Process interface {
	Wait() (int, error)
	Interrupt() error
	Kill() error
}

// Runner starts one process. Tests replace it with a deterministic fake.
type Runner interface {
	Start(Command, io.Writer, io.Writer) (Process, error)
}

type nativeRunner struct{}

func (nativeRunner) Start(in Command, stdout, stderr io.Writer) (Process, error) {
	cmd := exec.Command(in.Path, in.Args...)
	cmd.Dir = in.Dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &nativeProcess{cmd: cmd}, nil
}

type nativeProcess struct{ cmd *exec.Cmd }

func (p *nativeProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	if p.cmd.ProcessState == nil {
		return -1, err
	}
	return p.cmd.ProcessState.ExitCode(), err
}

func (p *nativeProcess) Interrupt() error { return p.cmd.Process.Signal(os.Interrupt) }
func (p *nativeProcess) Kill() error      { return p.cmd.Process.Kill() }
