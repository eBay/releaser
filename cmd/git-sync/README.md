git-sync
========

git-sync pulls source code from a git repository periodically and calls external
webhook automatically when there are changes.

## Install

```console
$ go get github.com/ebay/releaser/cmd/git-sync
```

## Github

```console
$ git-sync --branch=master --repo=https://github.com/ebay/releaser \
    --one-time --password-file=/home/stack/tmp/github-pwd --rev=HEAD \
    --username=tess-bot --wait=10 --root=/home/stack/tmp --one-time
```

## Gitlab

```console
$ git-sync --branch=master --repo=https://github.com/ebay/releaser.git \
    --one-time --password-file=/home/stack/tmp/gitlab-pwd --rev=HEAD \
    --username=oauth2 --wait=10 --root=/home/stack/tmp --one-time
```

## Checkout Pull Request

```console
$ git-sync --branch=master --repo=https://github.com/ebay/releaser \
    --one-time --password-file=/home/stack/tmp/github-pwd \
    --rev=refs/remotes/origin/pr/${PR}/merge \
    --username=tess-bot --wait=10 --root=/home/stack/tmp --one-time
```
