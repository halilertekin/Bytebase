package pg

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bytebase/bytebase/common"
	"github.com/bytebase/bytebase/plugin/db"
	"github.com/bytebase/bytebase/plugin/db/util"
)

var (
	excludedDatabaseList = map[string]bool{
		// Skip our internal "bytebase" database
		"bytebase": true,
		// Skip internal databases from cloud service providers
		// see https://github.com/bytebase/bytebase/issues/30
		// aws
		"rdsadmin": true,
		// gcp
		"cloudsql":      true,
		"cloudsqladmin": true,
	}
)

// pgDatabaseSchema describes a pg database schema.
type pgDatabaseSchema struct {
	name     string
	encoding string
	collate  string
}

// tableSchema describes the schema of a pg table.
type tableSchema struct {
	schemaName    string
	name          string
	tableowner    string
	comment       string
	rowCount      int64
	tableSizeByte int64
	indexSizeByte int64

	columns     []*columnSchema
	constraints []*tableConstraint
}

// columnSchema describes the schema of a pg table column.
type columnSchema struct {
	columnName             string
	dataType               string
	ordinalPosition        int
	characterMaximumLength string
	columnDefault          string
	isNullable             bool
	collationName          string
	comment                string
}

// tableConstraint describes constraint schema of a pg table.
type tableConstraint struct {
	name       string
	schemaName string
	tableName  string
	constraint string
}

// viewSchema describes the schema of a pg view.
type viewSchema struct {
	schemaName string
	name       string
	definition string
	comment    string
}

// indexSchema describes the schema of a pg index.
type indexSchema struct {
	schemaName string
	name       string
	tableName  string
	statement  string
	unique     bool
	primary    bool
	// methodType such as btree.
	methodType        string
	columnExpressions []string
	comment           string
}

// SyncInstance syncs the instance.
func (driver *Driver) SyncInstance(ctx context.Context) (*db.InstanceMeta, error) {
	version, err := driver.getVersion(ctx)
	if err != nil {
		return nil, err
	}

	// Query user info
	userList, err := driver.getUserList(ctx)
	if err != nil {
		return nil, err
	}

	// Skip all system databases
	for k := range systemDatabases {
		excludedDatabaseList[k] = true
	}
	// Query db info
	databases, err := driver.getDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get databases: %s", err)
	}
	var databaseList []db.DatabaseMeta
	for _, database := range databases {
		dbName := database.name
		if _, ok := excludedDatabaseList[dbName]; ok {
			continue
		}

		databaseList = append(
			databaseList,
			db.DatabaseMeta{
				Name:         dbName,
				CharacterSet: database.encoding,
				Collation:    database.collate,
			},
		)
	}

	return &db.InstanceMeta{
		Version:      version,
		UserList:     userList,
		DatabaseList: databaseList,
	}, nil
}

// SyncDBSchema syncs a single database schema.
func (driver *Driver) SyncDBSchema(ctx context.Context, databaseName string) (*db.Schema, error) {
	// Query db info
	databases, err := driver.getDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get databases: %s", err)
	}

	schema := db.Schema{
		Name: databaseName,
	}
	found := false
	for _, database := range databases {
		if database.name == databaseName {
			found = true
			schema.CharacterSet = database.encoding
			schema.Collation = database.collate
			break
		}
	}
	if !found {
		return nil, common.Errorf(common.NotFound, "database %q not found", databaseName)
	}

	sqldb, err := driver.GetDBConnection(ctx, databaseName)
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection for %q: %s", databaseName, err)
	}
	txn, err := sqldb.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer txn.Rollback()

	// Index statements.
	indicesMap := make(map[string][]*indexSchema)
	indices, err := getIndices(txn)
	if err != nil {
		return nil, fmt.Errorf("failed to get indices from database %q: %s", databaseName, err)
	}
	for _, idx := range indices {
		key := fmt.Sprintf("%s.%s", idx.schemaName, idx.tableName)
		indicesMap[key] = append(indicesMap[key], idx)
	}

	// Table statements.
	tables, err := getPgTables(txn)
	if err != nil {
		return nil, fmt.Errorf("failed to get tables from database %q: %s", databaseName, err)
	}
	for _, tbl := range tables {
		var dbTable db.Table
		dbTable.Name = fmt.Sprintf("%s.%s", tbl.schemaName, tbl.name)
		dbTable.Type = "BASE TABLE"
		dbTable.Comment = tbl.comment
		dbTable.RowCount = tbl.rowCount
		dbTable.DataSize = tbl.tableSizeByte
		dbTable.IndexSize = tbl.indexSizeByte
		for _, col := range tbl.columns {
			var dbColumn db.Column
			dbColumn.Name = col.columnName
			dbColumn.Position = col.ordinalPosition
			dbColumn.Default = &col.columnDefault
			dbColumn.Type = col.dataType
			dbColumn.Nullable = col.isNullable
			dbColumn.Collation = col.collationName
			dbColumn.Comment = col.comment
			dbTable.ColumnList = append(dbTable.ColumnList, dbColumn)
		}
		indices := indicesMap[dbTable.Name]
		for _, idx := range indices {
			for i, colExp := range idx.columnExpressions {
				var dbIndex db.Index
				dbIndex.Name = idx.name
				dbIndex.Expression = colExp
				dbIndex.Position = i + 1
				dbIndex.Type = idx.methodType
				dbIndex.Unique = idx.unique
				dbIndex.Primary = idx.primary
				dbIndex.Comment = idx.comment
				dbTable.IndexList = append(dbTable.IndexList, dbIndex)
			}
		}

		schema.TableList = append(schema.TableList, dbTable)
	}
	// View statements.
	views, err := getViews(txn)
	if err != nil {
		return nil, fmt.Errorf("failed to get views from database %q: %s", databaseName, err)
	}
	for _, view := range views {
		var dbView db.View
		dbView.Name = fmt.Sprintf("%s.%s", view.schemaName, view.name)
		// Postgres does not store
		dbView.CreatedTs = time.Now().Unix()
		dbView.Definition = view.definition
		dbView.Comment = view.comment

		schema.ViewList = append(schema.ViewList, dbView)
	}
	// Extensions.
	extensions, err := getExtensions(txn)
	if err != nil {
		return nil, fmt.Errorf("failed to get extensions from database %q: %s", databaseName, err)
	}
	schema.ExtensionList = extensions

	if err := txn.Commit(); err != nil {
		return nil, err
	}

	return &schema, err
}

func (driver *Driver) getUserList(ctx context.Context) ([]db.User, error) {
	// Query user info
	query := `
		SELECT usename AS role_name,
			CASE
				 WHEN usesuper AND usecreatedb THEN
				 CAST('superuser, create database' AS pg_catalog.text)
				 WHEN usesuper THEN
					CAST('superuser' AS pg_catalog.text)
				 WHEN usecreatedb THEN
					CAST('create database' AS pg_catalog.text)
				 ELSE
					CAST('' AS pg_catalog.text)
			END role_attributes
		FROM pg_catalog.pg_user
		ORDER BY role_name
			`
	var userList []db.User
	rows, err := driver.db.QueryContext(ctx, query)
	if err != nil {
		return nil, util.FormatErrorWithQuery(err, query)
	}
	defer rows.Close()

	for rows.Next() {
		var role string
		var attr string
		if err := rows.Scan(
			&role,
			&attr,
		); err != nil {
			return nil, err
		}

		userList = append(userList, db.User{
			Name:  role,
			Grant: attr,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return userList, nil
}

// getTables gets all tables of a database.
func getPgTables(txn *sql.Tx) ([]*tableSchema, error) {
	constraints, err := getTableConstraints(txn)
	if err != nil {
		return nil, fmt.Errorf("getTableConstraints() got error: %v", err)
	}

	var tables []*tableSchema
	query := "" +
		"SELECT tbl.schemaname, tbl.tablename, tbl.tableowner, pg_table_size(c.oid), pg_indexes_size(c.oid) " +
		"FROM pg_catalog.pg_tables tbl, pg_catalog.pg_class c " +
		"WHERE schemaname NOT IN ('pg_catalog', 'information_schema') AND tbl.schemaname=c.relnamespace::regnamespace::text AND tbl.tablename = c.relname;"
	rows, err := txn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var tbl tableSchema
		var schemaname, tablename, tableowner string
		var tableSizeByte, indexSizeByte int64
		if err := rows.Scan(&schemaname, &tablename, &tableowner, &tableSizeByte, &indexSizeByte); err != nil {
			return nil, err
		}
		tbl.schemaName = schemaname
		tbl.name = tablename
		tbl.tableowner = tableowner
		tbl.tableSizeByte = tableSizeByte
		tbl.indexSizeByte = indexSizeByte

		tables = append(tables, &tbl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, tbl := range tables {
		if err := getTable(txn, tbl); err != nil {
			return nil, fmt.Errorf("getTable(%q, %q) got error %v", tbl.schemaName, tbl.name, err)
		}
		columns, err := getTableColumns(txn, tbl.schemaName, tbl.name)
		if err != nil {
			return nil, fmt.Errorf("getTableColumns(%q, %q) got error %v", tbl.schemaName, tbl.name, err)
		}
		tbl.columns = columns

		key := fmt.Sprintf("%s.%s", tbl.schemaName, tbl.name)
		tbl.constraints = constraints[key]
	}
	return tables, nil
}

func getTable(txn *sql.Tx, tbl *tableSchema) error {
	countQuery := fmt.Sprintf(`SELECT COUNT(1) FROM "%s"."%s";`, tbl.schemaName, tbl.name)
	rows, err := txn.Query(countQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		if err := rows.Scan(&tbl.rowCount); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	commentQuery := fmt.Sprintf(`SELECT obj_description('"%s"."%s"'::regclass);`, tbl.schemaName, tbl.name)
	crows, err := txn.Query(commentQuery)
	if err != nil {
		return err
	}
	defer crows.Close()

	for crows.Next() {
		var comment sql.NullString
		if err := crows.Scan(&comment); err != nil {
			return err
		}
		tbl.comment = comment.String
	}
	return crows.Err()
}

// getTableColumns gets the columns of a table.
func getTableColumns(txn *sql.Tx, schemaName, tableName string) ([]*columnSchema, error) {
	query := `
	SELECT
		cols.column_name,
		cols.data_type,
		cols.ordinal_position,
		cols.character_maximum_length,
		cols.column_default,
		cols.is_nullable,
		cols.collation_name,
		cols.udt_schema,
		cols.udt_name,
		pg_catalog.col_description(c.oid, cols.ordinal_position::int) as column_comment
	FROM INFORMATION_SCHEMA.COLUMNS AS cols, pg_catalog.pg_class c
	WHERE table_schema=$1 AND table_name=$2 AND cols.table_schema=c.relnamespace::regnamespace::text AND cols.table_name=c.relname;`
	rows, err := txn.Query(query, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []*columnSchema
	for rows.Next() {
		var columnName, dataType, isNullable string
		var characterMaximumLength, columnDefault, collationName, udtSchema, udtName, comment sql.NullString
		var ordinalPosition int
		if err := rows.Scan(&columnName, &dataType, &ordinalPosition, &characterMaximumLength, &columnDefault, &isNullable, &collationName, &udtSchema, &udtName, &comment); err != nil {
			return nil, err
		}
		isNullBool, err := convertBoolFromYesNo(isNullable)
		if err != nil {
			return nil, err
		}
		c := columnSchema{
			columnName:             columnName,
			dataType:               dataType,
			ordinalPosition:        ordinalPosition,
			characterMaximumLength: characterMaximumLength.String,
			columnDefault:          columnDefault.String,
			isNullable:             isNullBool,
			collationName:          collationName.String,
			comment:                comment.String,
		}
		switch dataType {
		case "USER-DEFINED":
			c.dataType = fmt.Sprintf("%s.%s", udtSchema.String, udtName.String)
		case "ARRAY":
			c.dataType = udtName.String
		}
		columns = append(columns, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// getTableConstraints gets all table constraints of a database.
func getTableConstraints(txn *sql.Tx) (map[string][]*tableConstraint, error) {
	query := "" +
		"SELECT n.nspname, conrelid::regclass, conname, pg_get_constraintdef(c.oid) " +
		"FROM pg_constraint c " +
		"JOIN pg_namespace n ON n.oid = c.connamespace " +
		"WHERE n.nspname NOT IN ('pg_catalog', 'information_schema');"
	ret := make(map[string][]*tableConstraint)
	rows, err := txn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var constraint tableConstraint
		if err := rows.Scan(&constraint.schemaName, &constraint.tableName, &constraint.name, &constraint.constraint); err != nil {
			return nil, err
		}
		if strings.Contains(constraint.tableName, ".") {
			constraint.tableName = constraint.tableName[1+strings.Index(constraint.tableName, "."):]
		}
		key := fmt.Sprintf("%s.%s", constraint.schemaName, constraint.tableName)
		ret[key] = append(ret[key], &constraint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ret, nil
}

// getViews gets all views of a database.
func getViews(txn *sql.Tx) ([]*viewSchema, error) {
	query := "" +
		"SELECT schemaname, viewname, definition FROM pg_catalog.pg_views " +
		"WHERE schemaname NOT IN ('pg_catalog', 'information_schema');"
	var views []*viewSchema
	rows, err := txn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var view viewSchema
		var def sql.NullString
		if err := rows.Scan(&view.schemaName, &view.name, &def); err != nil {
			return nil, err
		}
		// Return error on NULL view definition.
		// https://github.com/bytebase/bytebase/issues/343
		if !def.Valid {
			return nil, fmt.Errorf("schema %q view %q has empty definition; please check whether proper privileges have been granted to Bytebase", view.schemaName, view.name)
		}
		view.definition = def.String
		views = append(views, &view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, view := range views {
		if err = getView(txn, view); err != nil {
			return nil, fmt.Errorf("getPgView(%q, %q) got error %v", view.schemaName, view.name, err)
		}
	}
	return views, nil
}

// getView gets the schema of a view.
func getView(txn *sql.Tx, view *viewSchema) error {
	query := fmt.Sprintf(`SELECT obj_description('"%s"."%s"'::regclass);`, view.schemaName, view.name)
	rows, err := txn.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var comment sql.NullString
		if err := rows.Scan(&comment); err != nil {
			return err
		}
		view.comment = comment.String
	}
	return rows.Err()
}

func getExtensions(txn *sql.Tx) ([]db.Extension, error) {
	query := "" +
		"SELECT e.extname, e.extversion, n.nspname, c.description " +
		"FROM pg_catalog.pg_extension e " +
		"LEFT JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace " +
		"LEFT JOIN pg_catalog.pg_description c ON c.objoid = e.oid AND c.classoid = 'pg_catalog.pg_extension'::pg_catalog.regclass " +
		"WHERE n.nspname != 'pg_catalog';"

	var extensions []db.Extension
	rows, err := txn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var e db.Extension
		if err := rows.Scan(&e.Name, &e.Version, &e.Schema, &e.Description); err != nil {
			return nil, err
		}
		extensions = append(extensions, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return extensions, nil
}

// getIndices gets all indices of a database.
func getIndices(txn *sql.Tx) ([]*indexSchema, error) {
	query := "" +
		"SELECT schemaname, tablename, indexname, indexdef " +
		"FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog', 'information_schema');"

	var indices []*indexSchema
	rows, err := txn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var idx indexSchema
		if err := rows.Scan(&idx.schemaName, &idx.tableName, &idx.name, &idx.statement); err != nil {
			return nil, err
		}
		idx.unique = strings.Contains(idx.statement, " UNIQUE INDEX ")
		idx.methodType = getIndexMethodType(idx.statement)
		idx.columnExpressions, err = getIndexColumnExpressions(idx.statement)
		if err != nil {
			return nil, err
		}
		indices = append(indices, &idx)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, idx := range indices {
		if err = getIndex(txn, idx); err != nil {
			return nil, fmt.Errorf("getIndex(%q, %q) got error %v", idx.schemaName, idx.name, err)
		}

		if err = getPrimary(txn, idx); err != nil {
			return nil, fmt.Errorf("getPrimary(%q, %q) got error %v", idx.schemaName, idx.name, err)
		}
	}

	return indices, nil
}

func getPrimary(txn *sql.Tx, idx *indexSchema) error {
	isPrimaryQuery := `
		SELECT count(*)
		FROM information_schema.table_constraints
		WHERE constraint_schema = $1
		  AND constraint_name = $2
		  AND table_schema = $1
		  AND table_name = $3
		  AND constraint_type = 'PRIMARY KEY'
	`

	var yes int
	if err := txn.QueryRow(isPrimaryQuery, idx.schemaName, idx.name, idx.tableName).Scan(&yes); err != nil {
		return err
	}

	idx.primary = (yes == 1)
	return nil
}

func getIndex(txn *sql.Tx, idx *indexSchema) error {
	commentQuery := fmt.Sprintf(`SELECT obj_description('"%s"."%s"'::regclass);`, idx.schemaName, idx.name)
	rows, err := txn.Query(commentQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var comment sql.NullString
		if err := rows.Scan(&comment); err != nil {
			return err
		}
		idx.comment = comment.String
	}
	return rows.Err()
}

func convertBoolFromYesNo(s string) (bool, error) {
	switch s {
	case "YES":
		return true, nil
	case "NO":
		return false, nil
	default:
		return false, fmt.Errorf("unrecognized isNullable type %q", s)
	}
}

func getIndexMethodType(stmt string) string {
	re := regexp.MustCompile(`USING (\w+) `)
	matches := re.FindStringSubmatch(stmt)
	if len(matches) == 0 {
		return ""
	}
	return matches[1]
}

func getIndexColumnExpressions(stmt string) ([]string, error) {
	rc := regexp.MustCompile(`\((.*)\)`)
	rm := rc.FindStringSubmatch(stmt)
	if len(rm) == 0 {
		return nil, fmt.Errorf("invalid index statement: %q", stmt)
	}
	columnStmt := rm[1]

	var cols []string
	re := regexp.MustCompile(`\(\(.*\)\)`)
	for {
		if len(columnStmt) == 0 {
			break
		}
		// Get a token
		token := ""
		// Expression has format of "((exp))".
		if strings.HasPrefix(columnStmt, "((") {
			token = re.FindString(columnStmt)
		} else {
			i := strings.Index(columnStmt, ",")
			if i < 0 {
				token = columnStmt
			} else {
				token = columnStmt[:i]
			}
		}
		// Strip token
		if len(token) == 0 {
			return nil, fmt.Errorf("invalid index statement: %q", stmt)
		}
		columnStmt = columnStmt[len(token):]
		cols = append(cols, strings.TrimSpace(token))

		// Trim space and remove a comma to prepare for the next tokenization.
		columnStmt = strings.TrimSpace(columnStmt)
		if len(columnStmt) > 0 && columnStmt[0] == ',' {
			columnStmt = columnStmt[1:]
		}
		columnStmt = strings.TrimSpace(columnStmt)
	}

	return cols, nil
}
