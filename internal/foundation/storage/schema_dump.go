package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DumpSchema renders sqlite_master in the canonical text form used by the
// golden schema files: one header per object followed by its verbatim SQL.
// Internal (implicit) indexes have NULL SQL and are rendered as
// "<internal index>".
func DumpSchema(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master ORDER BY name`)
	if err != nil {
		return "", fmt.Errorf("storage: dump schema: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var (
			typ, name string
			text      sql.NullString
		)
		if err := rows.Scan(&typ, &name, &text); err != nil {
			return "", fmt.Errorf("storage: dump schema: %w", err)
		}
		b.WriteString("-- " + typ + " " + name + "\n")
		if text.Valid {
			b.WriteString(text.String)
		} else {
			b.WriteString("<internal index>")
		}
		b.WriteString("\n\n")
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("storage: dump schema: %w", err)
	}
	return b.String(), nil
}
