#!/usr/bin/env bash
set -euo pipefail
# One-time local PostgreSQL administration. Creates only the two named test databases.
DB_USER="$(id -un)"
if psql --dbname=agent_runtime_day14_test -X -Atc 'SELECT current_database()' >/dev/null 2>&1 &&
   psql --dbname=agent_runtime_test -X -Atc 'SELECT current_database()' >/dev/null 2>&1; then
    echo 'PASS: dedicated test databases already accessible'
    exit 0
fi
# Fail promptly rather than wait invisibly for a password.
# If needed, run sudo -v in your terminal and invoke this script again.
sudo -n -u postgres psql --dbname=postgres -X -v ON_ERROR_STOP=1 --set=db_user="$DB_USER" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN', :'db_user')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'db_user')
\gexec
SELECT format('CREATE DATABASE agent_runtime_day14_test OWNER %I', :'db_user')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'agent_runtime_day14_test')
\gexec
SELECT format('CREATE DATABASE agent_runtime_test OWNER %I', :'db_user')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'agent_runtime_test')
\gexec
SQL
echo 'PASS: dedicated test databases are available'
