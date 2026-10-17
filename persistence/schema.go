package persistence

import _ "embed"

// TasksSchema is the initial tasks table definition. Run migrations explicitly.
//
//go:embed schema.sql
var TasksSchema string
