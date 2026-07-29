package command

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Request struct {
	Name        string
	Args        []string
	Directory   string
	Environment []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Capture     bool
}

type Result struct {
	Stdout string
	Stderr string
}

type Runner interface {
	Run(context.Context, Request) (Result, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, request Request) (Result, error) {
	executable, err := exec.LookPath(request.Name)
	if err != nil {
		return Result{}, fmt.Errorf("%s is not installed or not on PATH", request.Name)
	}
	cmd := exec.CommandContext(ctx, executable, request.Args...)
	cmd.Dir = request.Directory
	if len(request.Environment) > 0 {
		cmd.Env = append(os.Environ(), request.Environment...)
	}
	cmd.Stdin = request.Stdin

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if request.Capture {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	} else {
		cmd.Stdout = request.Stdout
		cmd.Stderr = request.Stderr
	}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, fmt.Errorf("%s %s: %s", request.Name, strings.Join(request.Args, " "), detail)
	}
	return Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
