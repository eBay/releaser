package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type Cli struct {
	kubeconfig string
	timeout    string
	verbose    bool
}

func NewCli(kubeconfig string, timeout time.Duration) Cli {
	return Cli{
		kubeconfig: kubeconfig,
		timeout:    timeout.Truncate(time.Second).String(),
		verbose:    true,
	}
}

func (c *Cli) AddRepo(name, url string) error {
	return c.Run(context.TODO(), "repo", "add", name, url)
}

func (c Cli) Test(ctx context.Context, release string) error {
	return c.Run(ctx, "test", release)
}

type Release struct {
	Name string `json:"name"`
}

func (c Cli) DependencyBuild(ctx context.Context, chart string) error {
	return c.Run(ctx, "dependency", "build", chart)
}

func (c Cli) Exist(ctx context.Context, release string) (bool, error) {
	output, err := c.Cmd(ctx, "ls", "-o", "json").Output()
	if err != nil {
		return false, fmt.Errorf("%s\nfailed to list helm releases: %s", string(output), err)
	}
	if c.verbose {
		fmt.Println(string(output))
	}

	releases := []Release{}
	err = json.Unmarshal(output, &releases)
	if err != nil {
		return false, fmt.Errorf("failed to unmarshal helm releases: %s", err)
	}

	for _, re := range releases {
		if re.Name == release {
			return true, nil
		}
	}

	return false, nil
}

func (c Cli) Upgrade(ctx context.Context, release string, chart string, values map[string]string, files []string, postRenderer string) error {
	params := []string{"upgrade", "--install", "--timeout", c.timeout, release, chart}
	for key, value := range values {
		params = append(params, "--set", key+"="+value)
	}
	for _, file := range files {
		params = append(params, "-f", file)
	}
	if postRenderer != "" {
		params = append(params, "--post-renderer", postRenderer)
	}
	return c.Run(ctx, params...)
}

func (c Cli) Diff(ctx context.Context, release string, chart string, values map[string]string, files []string) (bool, error) {
	params := []string{"diff", "upgrade", release, chart}
	for key, value := range values {
		params = append(params, "--set", key+"="+value)
	}
	for _, file := range files {
		params = append(params, "-f", file)
	}

	output, err := c.Cmd(ctx, params...).Output()
	if err != nil {
		return false, fmt.Errorf("%s\nfailed to run helm diff: %s", string(output), err)
	}
	if c.verbose {
		fmt.Println(string(output))
	}
	return len(output) != 0, nil
}

func (c Cli) Cmd(ctx context.Context, params ...string) *exec.Cmd {
	if c.kubeconfig != "" {
		params = append([]string{
			"--kubeconfig",
			c.kubeconfig,
		}, params...)
	}

	cmd := exec.CommandContext(ctx, "helm", params...)
	if c.verbose {
		fmt.Fprintf(os.Stdout, "cmd: %v\n", cmd.Args)
	}
	cmd.Stderr = os.Stderr
	return cmd
}

func (c Cli) Run(ctx context.Context, params ...string) error {
	cmd := c.Cmd(ctx, params...)
	cmd.Stdout = os.Stdout
	return cmd.Run()
}
