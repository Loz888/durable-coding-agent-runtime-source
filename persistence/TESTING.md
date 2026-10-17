# PostgreSQL persistence tests

See [the complete test setup](../docs/testing.md). Set TEST_DATABASE_URL to the
dedicated agent_runtime_test database, where the test role can create schemas.
The tests create/drop random runtime_test_* schemas and apply schema.sql there.
They cover connection, Create/SELECT, state Update and HTTP-to-Collector persistence.

The day8-server command requires DATABASE_URL and uses NewServiceWithStore.
Without TEST_DATABASE_URL some legacy tests SKIP; the release test runner treats
every test SKIP as a failure rather than reporting database validation as green.
