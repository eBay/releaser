package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"

	"github.com/ebay/releaser/pkg/git"
	utilcmd "github.com/ebay/releaser/pkg/util/cmd"
	"github.com/ebay/releaser/pkg/webhook"
)

// git-sync is a sidecar which pulls git repository. if we specify branch as the
// parameter, it will run as a daemon, and pull the latest commits periodically.
//
// otherwise it just pulls once for commit/tag/pullrequests.
//
// we are trying to preserve the compatibility with kubernetes/git-sync, so we'll
// reuse the same set of flags:
//
//   - -repo=https://github.com/ebay/releaser.git
//   - -branch=verify-git-crypt
//   - -root=/git
//   - -username=oauth2
//   - -webhook-url=http://127.0.0.1:8080
//   - -webhook-timeout=5m
//   - -wait=600
//
// And GIT_SYNC_PASSWORD env is respected as password.

var (
	branch            string   // branch is the name of branch we are polling.
	repo              string   // repo is the repository url, to ssh key, the url should looks like ssh://git@github.corp.ebay.com/tess/tess-master.git
	root              string   // root is the parent directory where source code is pulled.
	rev               string   // rev is the tag or revision we are going to checkout
	includes          []string // includes is the path that we wish to checkout
	excludes          []string // excludes is the path that needs to be excluded in git-sync
	gpgKey            string   // gpgKey is a secret we should pull so that we can decrypt.
	username          string   // username is the git username
	passwordFile      string   // passwordFile is the path to a file where password is saved.
	ssh               bool     // if we want to enable ssh as auth mehtod.
	sshKeyFile        string   // the path to ssh private key file
	sshKnownHostsFile string   // the path to ssh knownhosts file, if unspecified, we will skip host key verification.

	webhookURL     string
	webhookTimeout time.Duration
	wait           int

	httpBind    string
	httpMetrics bool

	oneTime bool // exit after the initial checkout

	importedgpgkey bool
)

const (
	// VersionFile is the file name where we can read version information.
	VersionFile = "VERSION"
)

func init() {
	// git related variables
	pflag.StringVar(&branch, "branch", "master", "the name of branch we are polling")
	pflag.StringVar(&repo, "repo", "", "the repository url where we are pulling from, for ssh url should looks like ssh://git@github.corp.ebay.com/tess/tess-master.git")
	pflag.StringVar(&root, "root", "", "the parent directory where source code is cloned")
	pflag.StringVar(&rev, "rev", "HEAD", "the revision we are going to checkout")
	pflag.StringArrayVar(&excludes, "excludes", []string{}, "the files or directories which should not be checked out")
	pflag.StringArrayVar(&includes, "includes", []string{"/*"}, "the files or directories which should be checked out")
	pflag.StringVar(&username, "username", "", "the username for git authentication")
	pflag.StringVar(&passwordFile, "password-file", "", "the path to password file")
	pflag.StringVar(&gpgKey, "gpg-key", "", "the path to the gpg key")
	pflag.BoolVar(&ssh, "ssh", false, "if we want to use ssh private key")
	pflag.StringVar(&sshKeyFile, "ssh-key-file", "", "the path to ssh key file")
	pflag.StringVar(&sshKnownHostsFile, "ssh-known-hosts-file", "", "the path to ssh key file")

	// webhook related variables.
	pflag.StringVar(&webhookURL, "webhook-url", "", "the url of webhook which receives events")
	pflag.DurationVar(&webhookTimeout, "webhook-timeout", 3*time.Second, "the timeout when calling webhook")
	pflag.IntVar(&wait, "wait", 600, "number of seconds before polling next time")

	pflag.StringVar(&httpBind, "http-bind", ":9092", "the bind address (including port) for git-sync's HTTP endpoint")
	pflag.BoolVar(&httpMetrics, "http-metrics", true, "enable metrics on git-sync's HTTP endpoint")

	pflag.BoolVar(&oneTime, "one-time", false, "exit after the initial checkout")
}

func main() {
	pflag.Parse()

	// start the http server for metrics (and others in future)
	if httpBind != "" {
		ln, err := net.Listen("tcp", httpBind)
		if err != nil {
			log.Fatalf("failed to bind HTTP endpoint: %s", err)
		}
		mux := http.NewServeMux()
		go func() {
			if httpMetrics {
				mux.Handle("/metrics", promhttp.Handler())
			}
			http.Serve(ln, mux)
		}()
	}

	// gpg defaults to ~/.gnupg. This requires a writtable HOME directory.
	// However git-sync might run as an arbitrary non root user and HOME
	// directory may not always be available. In this case we change it to
	// be the ${root}/.gnugp to overcome this limitation.
	os.Setenv("GNUPGHOME", filepath.Join(root, ".gnupg"))

	// Depending on the repository size, the initial setup may take as long
	// as it needs. To work with decent size of repositories, 10 minutes is
	// supposed to be sufficient, but we can always revisit later.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// try run gpg --import gpgkey
	if gpgKey != "" {
		err := utilcmd.Run(ctx, "gpg", "--import", gpgKey)
		if err == nil {
			importedgpgkey = true
		}
	}

	var password string
	var pwcensor string
	if passwordFile != "" {
		data, err := ioutil.ReadFile(passwordFile)
		if err != nil {
			log.Fatalf("failed to read %s: %s", passwordFile, err)
		}
		password = string(data)
		password = strings.TrimSuffix(password, "\n")
		pwcensor = fmt.Sprintf("$(cat %s)", passwordFile)
	} else if !ssh {
		// otherwise fallback to read Env
		password = os.Getenv("GIT_SYNC_PASSWORD")
		if password == "" {
			log.Fatalf("flag --password-file or env GIT_SYNC_PASSWORD is not set")
		}
		pwcensor = "${GIT_SYNC_PASSWORD}"
	}

	git := git.New(true, func(v string) string {
		return strings.ReplaceAll(v, password, pwcensor)
	})

	if strings.HasPrefix(repo, "git@") {
		repo = "ssh://" + strings.Replace(repo, ":", "/", 1)
	}
	repoURL, err := url.Parse(repo)
	if err != nil {
		log.Fatalf("failed to parse repo %s: %s", repo, err)
	}
	if !ssh {
		repoURL.User = url.UserPassword(username, password)
	} else {
		os.Setenv("HOME", root)
		_, err := ioutil.ReadFile(sshKeyFile)
		if err != nil {
			log.Fatalf("failed to read %s: %s, please set up sshKeyFile or provide valid path", sshKeyFile, err)
		}
		sshCmd := fmt.Sprintf("ssh -i %s", sshKeyFile)
		if sshKnownHostsFile != "" {
			sshCmd += fmt.Sprintf(" -o UserKnownHostsFile=%s", sshKnownHostsFile)
		} else {
			sshCmd += " -o StrictHostKeyChecking=accept-new"
		}
		err = git.Run(ctx, "config", "--global", "core.sshCommand", sshCmd)
		if err != nil {
			log.Fatalf("error configuring ssh private key/host key checking: %s", err)
		}
	}

	// repodir is the directory which our worktree is linked to.
	repodir := filepath.Join(root, repo[strings.LastIndex(repo, "/")+1:])
	// workdir is the directory where we are going to setup git.
	workdir := filepath.Join(root, ".git-sync")

	worktree, err := filepath.EvalSymlinks(repodir)
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("failed to eval symbol link %s: %s", repodir, err)
	}
	files, err := ioutil.ReadDir(root)
	if err != nil {
		log.Fatalf("failed to read dir %s: %s", root, err)
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		match, err := regexp.MatchString("^\\.[0-9a-f]{40}$", file.Name())
		if err != nil {
			log.Fatalf("failed to match %s: %s", file.Name(), err)
		}
		if !match {
			continue
		}
		if strings.HasSuffix(worktree, file.Name()) {
			continue
		}
		err = os.RemoveAll(filepath.Join(root, file.Name()))
		if err != nil {
			log.Fatalf("failed to remove %s: %s", file.Name(), err)
		}
	}

	var client = webhook.New(webhookURL, webhookTimeout, 5*time.Second)

	var head string
	if worktree == "" {
		err = os.RemoveAll(workdir)
		if err != nil {
			fatal(fmt.Sprintf("failed to remove %s: %s", workdir, err), webhookURL != "", client)
		}
		err = git.Run(ctx, "clone", repoURL.String(), fmt.Sprintf("--branch=%s", branch), workdir)
		if err != nil {
			fatal(fmt.Sprintf("failed to clone repo %s: %s", workdir, err), webhookURL != "", client)
		}
	} else {
		head = worktree[len(worktree)-40:]
	}

	for {
		commit, tag, version := sync(git, repodir, workdir, head)
		if commit != head {
			head = commit
		}
		// call webhook if it is configured.
		if webhookURL != "" {
			client.Call(commit, tag, strings.TrimSpace(version))
		}
		if oneTime {
			os.Exit(0)
		}
		time.Sleep(time.Duration(wait) * time.Second)
	}
}

func sync(git git.Git, repodir, workdir, head string) (commit, tag, version string) {
	// change directory into workdir.
	err := os.Chdir(workdir)
	if err != nil {
		log.Fatalf("failed to change dir to %s: %s", workdir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// enable workTree config so that we can run git-crypt under work tree.
	err = git.Run(ctx, "config", "extensions.worktreeConfig", "true")
	if err != nil {
		log.Fatalf("failed to config extensions.worktreeConfig: %s", err)
	}

	// fetch everything from the remote branch, including tags
	err = git.Run(ctx, "fetch", "--force", "--tags", "origin", branch)
	if err != nil {
		log.Fatalf("failed to run pull %s: %s", branch, err)
	}

	// reset the current branch to match with remote.
	err = git.Run(ctx, "reset", "--hard", "origin/"+branch)
	if err != nil {
		log.Fatalf("failed to reset branch: %s", err)
	}

	// fetch pull requests as well if revision is a pull request
	if strings.HasPrefix(rev, "refs/remotes/origin/pr/") {
		err = git.Run(ctx, "fetch", "--force", "origin", "refs/pull/*:refs/remotes/origin/pr/*")
		if err != nil {
			log.Fatalf("failed to fetch pull requests: %s", err)
		}
	}

	// parse the revision as a commit.
	output, err := git.Cmd(ctx, "rev-parse", rev+"^{}").Output()
	if err != nil {
		log.Fatalf("%s\nfailed to run rev-parse %s: %s", string(output), rev, err)
	}
	commit = strings.TrimSpace(string(output))
	// commitdir is the worktree path for a specific commit id.
	commitdir := filepath.Join(root, "."+commit)

	output, err = git.Cmd(ctx, "describe", "--tags", "--abbrev=0", commit).Output()
	if err == nil {
		tag = strings.TrimSpace(string(output))
	}

	// This will prune any worktrees that aren't present anymore.
	err = git.Run(ctx, "worktree", "prune")
	if err != nil {
		log.Fatalf("failed to prune worktree: %s", err)
	}

	// see a newer commit or first-time setup: initialize the worktree
	if head != commit {
		err = git.Run(ctx, "worktree", "add", "-f", commitdir, commit)
		if err != nil {
			log.Fatalf("failed to add worktree %s: %s", commitdir, err)
		}
		err = checkout(ctx, git, commitdir)
		if err != nil {
			log.Fatalf("failed to checkout %s: %s", commit, err)
		}

		err = updateLink(ctx, git, commitdir, repodir)
		if err != nil {
			log.Fatalf("failed to update link %s to %s: %s", repodir, commitdir, err)
		}
	}

	versionData, err := ioutil.ReadFile(filepath.Join(commitdir, VersionFile))
	if err != nil && os.IsNotExist(err) {
		version = ""
	} else if err != nil {
		log.Fatalf("failed to read %s: %s", VersionFile, err)
	} else {
		version = strings.TrimSpace(string(versionData))
	}
	return commit, tag, version
}

func checkout(ctx context.Context, git git.Git, commitdir string) error {
	err := os.Chdir(commitdir)
	if err != nil {
		return fmt.Errorf("failed to change dir to %s: %s", commitdir, err)
	}

	// At this point, the repository has been fully checked out. We have to
	// run git-crypt before configuring sparse checkout. If we do it otherwise,
	// then it is likely some encrypted files/directories are skipped and thus
	// fails git-crypt unlock.
	info, err := os.Stat(".git-crypt")
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to check .git-crypt existence: %s", err)
	}
	if err == nil && info.IsDir() {
		args := []string{"unlock"}
		if !importedgpgkey {
			args = append(args, gpgKey)
		}
		// run git-crypt unlock
		err = utilcmd.Run(ctx, "git-crypt", args...)
		if err != nil {
			return fmt.Errorf("failed to run git-crypt unlock: %s", err)
		}
	}

	err = git.Run(ctx, "config", "--worktree", "core.sparseCheckout", "true")
	if err != nil {
		return fmt.Errorf("failed to enable sparse-checkout: %s", err)
	}

	dotgit, err := ioutil.ReadFile(".git")
	if err != nil {
		return fmt.Errorf("failed to read .git: %s", err)
	}
	// gitdir: /path/got/git/dir
	gitdir := strings.TrimSpace(string(dotgit[8:]))

	err = os.MkdirAll(filepath.Join(gitdir, "info"), 0755)
	if err != nil {
		return fmt.Errorf("failed to create info directory: %s", err)
	}

	sparseCheckoutFile, err := os.Create(filepath.Join(gitdir, "info", "sparse-checkout"))
	if err != nil {
		return fmt.Errorf("failed to create sparse-checkout file: %s", err)
	}
	defer sparseCheckoutFile.Close()

	for _, path := range includes {
		_, err = fmt.Fprintf(sparseCheckoutFile, "%s\n", os.ExpandEnv(path))
		if err != nil {
			return fmt.Errorf("failed to configure includes in sparse-checkout: %s", err)
		}
	}
	for _, path := range excludes {
		_, err = fmt.Fprintf(sparseCheckoutFile, "!%s\n", os.ExpandEnv(path))
		if err != nil {
			return fmt.Errorf("failed to configure excludes in sparse-checkout: %s", err)
		}
	}

	err = git.Run(ctx, "checkout")
	if err != nil {
		return fmt.Errorf("failed to checkout: %s", err)
	}
	return nil
}

// updateLink flips the link of repodir to let it point to the new commit dir.
func updateLink(ctx context.Context, git git.Git, commitdir, repodir string) error {
	predir, err := filepath.EvalSymlinks(repodir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to eval symbol link of %s: %s", repodir, err)
	}

	var tmplink = filepath.Join(root, ".tmp")
	err = utilcmd.Run(ctx, "ln", "-snf", commitdir, tmplink)
	if err != nil {
		return fmt.Errorf("failed to create link from %s to %s: %s", commitdir, tmplink, err)
	}
	err = utilcmd.Run(ctx, "mv", "-T", tmplink, repodir)
	if err != nil {
		return fmt.Errorf("failed to mv %s to %s: %s", tmplink, repodir, err)
	}

	// This won't return an error when predir is empty.
	// See https://github.com/golang/go/issues/28830
	err = os.RemoveAll(predir)
	if err != nil {
		return fmt.Errorf("failed to remove %s: %s", predir, err)
	}
	return nil
}

func fatal(message string, sendEvent bool, client webhook.Client) {
	if sendEvent {
		client.CallWithErrorMessage(fmt.Sprintf("failed to run git-sync: %s", message))
	}
	log.Fatalf(message)
}
