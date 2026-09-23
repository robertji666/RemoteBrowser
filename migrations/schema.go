// Package migrations contains the SQL embedded into the manager migration runner.
package migrations

import _ "embed"

// PersistentUsersSchema is applied once inside the versioned migration transaction.
// Backfilling deployment-specific quotas and legacy desired states is performed
// by Store.MigrateWithDefaults in the same transaction.
//
//go:embed 0003_persistent_users.sql
var PersistentUsersSchema string
