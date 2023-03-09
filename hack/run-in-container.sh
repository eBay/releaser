#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

PACKAGE=github.com/ebay/releaser

# abspath is copied from:
# https://superuser.com/questions/205127/how-to-retrieve-the-absolute-path-of-an-arbitrary-file-from-the-os-x
function abspath() {
  pushd . >/dev/null
  if [ -d "$1" ]; then
    cd "$1"
    dirs -l +0
  else
    cd "$(dirname \"$1\")"
    cur_dir=$(dirs -l +0)
    if [ "$cur_dir" == "/" ]; then
      echo "$cur_dir$(basename \"$1\")"
    else
      echo "$cur_dir/$(basename \"$1\")"
    fi
  fi
  popd >/dev/null
}

ROOT=$(abspath $(dirname "${BASH_SOURCE}")/..)

docker run --rm -v ${ROOT}:/go/src/${PACKAGE} \
  --user=$(id -u):$(id -g) \
  --workdir=/go/src/${PACKAGE} \
  ghcr.io/ebay/releaser/code-generator:v1.26 $1
