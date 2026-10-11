#!/usr/bin/env bash
exec "$(dirname "$0")/terraform-destroy.sh" "$@"
