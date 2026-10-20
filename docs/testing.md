# Testing and reproduction

[English](testing.md) | [简体中文](zh-CN/testing.md) | [Home](../README.md)

Use Linux/WSL2, the Go version in go.mod, Git, Bash, Python3 and PostgreSQL16.
README Quick Start creates agent_runtime_day14_test and agent_runtime_test.
The role must own test databases or be able to create/drop schemas.

Export DAY12_TEST_DATABASE_URL and TEST_DATABASE_URL. Never use production
DATABASE_URL for tests. setup_test_db.sh checks exact names, applies 001–006
to the queue test database, and leaves task schema creation to isolated
persistence tests. TCP DSNs can use TEST_USER:TEST_PASSWORD at localhost with
the same database names. CI's ci-only-password is a disposable service-container
credential, not a deployment or user secret.

## Suites
- test_unit.sh runs complete PostgreSQL-independent package suites: cmd/day21-eval, eval21, distributed,
  eval16, executor, internal/testdb, observability, repair17/18/20, repodiag and
  runtimecore. Mixed packages run fully in the integration job.
- test.sh ./... runs every root-module test with real PostgreSQL.
- test.sh -race ./... runs the same suite under the race detector.
- go test -count=10 -timeout 900s ./repair20 ./durable ./repair19 repeats the core packages.
- go test -count=1 -timeout 300s ./durable ./repair19 ./internal/integration20
  exercises combined authorization, persistence and recovery.

Fixtures own rows and random Patch schemas. Independent fixtures sharing the
fixed day12 queue serialize via an advisory lock; within-fixture owner/renewal
and Patch-lock competition stays real. Child helper tests return normally
unless started by the fault parent; parents execute them in actual subprocesses.
This is not SKIP.

## No-SKIP acceptance
test.sh records Go JSON events and check_test_results.py rejects failed/skipped
tests, package failure, zero test results or absent required demo scenarios.
Some original optional DB helpers still SKIP without URLs; that is not release
acceptance. CI and release tests provide both connections and reject every SKIP.

## Models and intentional failures
Mock uses ScriptedFix for the controlled Divide defect; HTTP model tests use
local httptest. These are not a real DeepSeek evaluation. Real-model runs are
opt-in and never CI or these demos.

Four nested evaldata/fixtures modules intentionally fail tests as oracle inputs;
do not delete them or fix their defects to make an evaluation appear green.
They are excluded from root-module traversal by their own go.mod files.

## Cold compiler caches in race CI

The race job first compiles normal test dependencies with go test -run '^$'.
Go's race-instrumented standard library cache differs from the cache used by
the nested ordinary go test commands in trusted fixtures. Warming the latter
prevents a fresh runner's compiler work from consuming a fixture's strict
execution timeout. This preparation executes no acceptance cases; the full
race suite still executes every test, checks JSON results and rejects SKIP.
Runtime deadlines, Lease guards and all assertions remain unchanged.

## Day 21 and output isolation

day21_accept.sh captures exactly the 12 required TestDay21Capture subtests,
recomputes committed summary/report files and scores a fresh dataset.
The full suite, Mock/crash demos and unit runner unset DAY21_RUNS_OUT to avoid
appending duplicate observations to another capture. The accept script passes
that variable only to the one capture command.

Ordinary/race/coverage use a 360-second timeout; ten repetitions of core fault
tests use 900 seconds. Timeouts do not remove any test or assertion.
See [evaluation](evaluation.md) for report reproduction and fresh capture.

## Pilot integration database roles

The new pilot API fixtures provision a separate restricted login with a random fixture-only password, including TCP/SCRAM CI. Their setup connection requires authority to create/manage test roles and grant access inside the two dedicated test databases. Run them in an isolated test PostgreSQL cluster or the provided CI service. Do not promote the production API role to run tests. The API itself refuses superuser/owner credentials.
