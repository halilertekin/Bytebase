package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/bytebase/bytebase/plugin/db"
	"github.com/bytebase/bytebase/resources/mysqlutil"
	"github.com/bytebase/bytebase/resources/postgres"

	// install mysql driver.
	_ "github.com/bytebase/bytebase/plugin/db/mysql"
	// install pg driver.
	_ "github.com/bytebase/bytebase/plugin/db/pg"
	"github.com/xo/dburl"
)

func getDatabase(u *dburl.URL) string {
	if u.Path == "" {
		return ""
	}
	return u.Path[1:]
}

func open(ctx context.Context, u *dburl.URL) (db.Driver, error) {
	var dbType db.Type
	var pgInstanceDir string
	resourceDir := os.TempDir()
	switch u.Driver {
	case "mysql":
		dbType = db.MySQL
		// dburl.Parse() parses 'pg', 'postgresql' and 'pgsql' to 'postgres'.
		// https://pkg.go.dev/github.com/xo/dburl@v0.9.1#hdr-Protocol_Schemes_and_Aliases
		if err := mysqlutil.Install(resourceDir); err != nil {
			return nil, fmt.Errorf("cannot install mysqlutil in directory %q, error: %w", resourceDir, err)
		}
	case "postgres":
		dbType = db.Postgres
		pgInstance, err := postgres.Install(resourceDir, "" /* pgDataDir */, "" /* pgUser */)
		if err != nil {
			return nil, fmt.Errorf("cannot install postgres in directory %q, error: %w", resourceDir, err)
		}
		pgInstanceDir = pgInstance.BaseDir
	default:
		return nil, fmt.Errorf("database type %q not supported; supported types: mysql, pg", u.Driver)
	}
	passwd, _ := u.User.Password()
	driver, err := db.Open(
		ctx,
		dbType,
		db.DriverConfig{
			PgInstanceDir: pgInstanceDir,
			ResourceDir:   resourceDir,
		},
		db.ConnectionConfig{
			Host:     u.Hostname(),
			Port:     u.Port(),
			Username: u.User.Username(),
			Password: passwd,
			Database: getDatabase(u),
			TLSConfig: db.TLSConfig{
				SslCA:   u.Query().Get("ssl-ca"),
				SslCert: u.Query().Get("ssl-cert"),
				SslKey:  u.Query().Get("ssl-key"),
			},
		},
		db.ConnectionContext{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to open database, got error: %w", err)
	}

	return driver, nil
}
