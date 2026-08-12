package util

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/user/uclaw/config"
	_ "modernc.org/sqlite"
)

type Database struct {
	dbPath string
	db     *sql.DB
}

// GetDefaultDBPath returns the default database path under ~/.mini_code/
func GetDefaultDBPath() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent.db"), nil
}

func NewDatabase(dbPath string) (*Database, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return &Database{dbPath: dbPath, db: db}, nil
}

// NewDefaultDatabase creates a new database using the default path under ~/.mini_code/
func NewDefaultDatabase() (*Database, error) {
	dbPath, err := GetDefaultDBPath()
	if err != nil {
		return nil, err
	}
	return NewDatabase(dbPath)
}

func (d *Database) Execute(sqlStmt string, params ...interface{}) (sql.Result, error) {
	return d.db.Exec(sqlStmt, params...)
}

func (d *Database) FetchOne(sqlStmt string, params ...interface{}) (map[string]interface{}, error) {
	rows, err := d.db.Query(sqlStmt, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, nil
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	result := make(map[string]interface{}, len(cols))
	for i, col := range cols {
		if b, ok := vals[i].([]byte); ok {
			result[col] = string(b)
		} else {
			result[col] = vals[i]
		}
	}
	return result, nil
}

func (d *Database) FetchAll(sqlStmt string, params ...interface{}) ([]map[string]interface{}, error) {
	rows, err := d.db.Query(sqlStmt, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			if b, ok := vals[i].([]byte); ok {
				m[col] = string(b)
			} else {
				m[col] = vals[i]
			}
		}
		results = append(results, m)
	}
	return results, nil
}

func (d *Database) InitTable(createSQL string) {
	d.db.Exec(createSQL)
}

func (d *Database) InitIndex(indexSQL string) {
	d.db.Exec(indexSQL)
}

func (d *Database) Close() error {
	return d.db.Close()
}
