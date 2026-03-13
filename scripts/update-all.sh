#!/bin/bash

# --- DEFAULTS ---
PROTO_BRANCH="main"
UTIL_BRANCH="main"
AUDIT_BRANCH="main"

# --- HELPER FUNCTIONS ---
info() {
    echo
    echo "#################################################################"
    echo "## $1"
    echo "#################################################################"
    echo
}

usage() {
    echo "Usage: $(basename "$0") [options]"
    echo
    echo "This script builds, deploys, and configures the telara-middleware services."
    echo
    echo "Options:"
    echo "  --proto-branch, -pb <branch> Update the protobuf definitions for all modules."
    echo "  --util-branch, -ub <branch> Update the utilities for all modules."
    echo "  --audit-branch, -ab <branch> Update the audit service for all modules."
    echo "  -h, --help              Display this help message."
    exit 1
}

# --- ARGUMENT PARSING ---
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --proto-branch|-pb) PROTO_BRANCH="$2"; shift ;;
        --util-branch|-ub) UTIL_BRANCH="$2"; shift ;;
        --audit-branch|-ab) AUDIT_BRANCH="$2"; shift ;;
        -h|--help) usage ;;
        *) echo "Unknown parameter passed: $1"; usage ;;
    esac
    shift
done

find . -type f -name "go.mod" -print0 | while IFS= read -r -d $'\0' file; do
  (
    cd "$(dirname "$file")" || exit 1
    # if telara module is in the file, then run the update-proto.sh script
    if grep -q "gitlab.com/telara-labs" "go.mod"; then
      echo "================================================"
      echo "Checking for updates in: $(pwd)"
      echo "================================================"
       # OK -  Updating module: gitlab.com/telara-labs/telara-utilities/go/security
       # NOT OK - Updating module: v0.0.0-20250714231219-3fded3728ec2
      # for each telara gitlab module - update the module to the main branch
      for module in $(grep -o "gitlab.com/telara-labs/telara-[a-z\-\/]*" "go.mod"); do
        echo "Updating module: $module"
        if [[ $module == *"telara-utilities"* ]]; then
          go get $module@$UTIL_BRANCH
        elif [[ $module == *"telara-audit"* ]]; then
          go get $module@$AUDIT_BRANCH
        elif [[ $module == *"telara-proto"* ]]; then
          go get $module@$PROTO_BRANCH
        else
          go get $module@main
        fi
      done
    else
      echo "Skipping module in: $(pwd)"
    fi
  )
done