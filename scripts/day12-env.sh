#!/usr/bin/env bash
# Source this file so the variables are available in your current terminal:
# source scripts/day12-env.sh
# Keep explicitly configured connection strings; otherwise use local peer authentication.
export DAY12_DATABASE_URL="${DAY12_DATABASE_URL:-host=/var/run/postgresql dbname=agent_runtime user=$(id -un) sslmode=disable}"
