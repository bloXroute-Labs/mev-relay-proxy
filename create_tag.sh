#!/usr/bin/env bash

# Sample command to create tag: `./create_tag.sh v0.0.0`

FULL_VERSION=${1:-latest}-$(git rev-parse --short HEAD)

echo $FULL_VERSION
if [[ ${FULL_VERSION} != v* ]]; then
  echo "version should start with v"
  exit 1
fi

if [[ `git status  | grep "git add <file>"` ]]; then
  echo "There are uncommited files. Are you sure you want to continue the build?"
  echo "Hit enter to continue or ctrl-c to quit."
  read
fi

git tag $FULL_VERSION
git push git@github.com:bloXroute-Labs/mev-relay-proxy-internal.git $FULL_VERSION --tag
