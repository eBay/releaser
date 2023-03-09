package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func New(ctx context.Context, name string, params ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, params...)
	cmd.Stderr = os.Stderr
	fmt.Fprintln(os.Stdout, fmt.Sprintf("cmd: %v", cmd.Args))
	return cmd
}

func Run(ctx context.Context, name string, params ...string) error {
	cmd := New(ctx, name, params...)
	cmd.Stdout = os.Stdout
	return cmd.Run()
}
