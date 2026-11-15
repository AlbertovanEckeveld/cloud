// Package migrations embeds the SQL migrations of every module, one directory
// per module. The directory name is also the module's PostgreSQL schema.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed */*.sql
var files embed.FS

// For returns the migrations of one module.
func For(module string) (fs.FS, error) {
	sub, err := fs.Sub(files, module)
	if err != nil {
		return nil, fmt.Errorf("migrations for %s: %w", module, err)
	}
	return sub, nil
}
