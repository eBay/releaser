package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/ebay/releaser/pkg/writer"
)

// Git is the command line interface for git.
type Git struct {
	verbose bool
	censor  func(string) string
	output  io.Writer
}

// New creates one instance of git cli.
func New(verbose bool, censor func(string) string) Git {
	return Git{
		verbose: verbose,
		censor:  censor,
		output:  writer.NewCensoredWriter(os.Stdout, censor),
	}
}

// Cmd creates a new git command.
func (g Git) Cmd(ctx context.Context, params ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", params...)
	if g.verbose {
		fmt.Fprintln(os.Stdout, g.censor(fmt.Sprintf("cmd: %v", cmd.Args)))
	}
	cmd.Stderr = g.output
	return cmd
}

// Run runs a command.
func (g Git) Run(ctx context.Context, params ...string) error {
	cmd := g.Cmd(ctx, params...)
	cmd.Stdout = g.output
	return cmd.Run()
}
