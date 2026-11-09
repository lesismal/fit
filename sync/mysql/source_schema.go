package syncmysql

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/sync/mysql/binlog"
)

// tableInfo is a table's columns as information_schema has them, in order,
// and its primary key's columns, by index.
type tableInfo struct {
	cols []colInfo
	pk   []int
}

type colInfo struct {
	name     string
	dataType string
	unsigned bool
	binary   bool
	// enum and set are the values of an ENUM or a SET.
	enum []string
}

// The character set the source puts on a column information_schema says
// is of characters: the source keeps a string's bytes as they are, so only
// binary or not matters.
const charsetText = 45 // utf8mb4_general_ci

// readTable reads table db.name's columns and primary key from
// information_schema; no columns when there is no such table.
func readTable(ctx context.Context, c srcConn, db, name string) (*tableInfo, error) {
	_, rows, err := c.QueryAll(ctx, "SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, COLLATION_NAME FROM information_schema.COLUMNS "+
		"WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", db, name)
	if err != nil {
		return nil, fmt.Errorf("mysql: the columns of %s.%s: %w", db, name, err)
	}
	ti := &tableInfo{}
	for _, r := range rows {
		ci := colInfo{name: str(r[0]), dataType: strings.ToLower(str(r[1]))}
		ct := strings.ToLower(str(r[2]))
		ci.unsigned = strings.Contains(ct, " unsigned")
		switch ci.dataType {
		case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "geometry", "point", "linestring",
			"polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection", "geomcollection", "vector":
			ci.binary = r[3] == nil
		case "enum", "set":
			ci.enum = enumValues(str(r[2]))
		}
		ti.cols = append(ti.cols, ci)
	}
	_, rows, err = c.QueryAll(ctx, "SELECT COLUMN_NAME FROM information_schema.STATISTICS "+
		"WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = 'PRIMARY' ORDER BY SEQ_IN_INDEX", db, name)
	if err != nil {
		return nil, fmt.Errorf("mysql: the primary key of %s.%s: %w", db, name, err)
	}
	for _, r := range rows {
		if i := ti.index(str(r[0])); i >= 0 {
			ti.pk = append(ti.pk, i)
		}
	}
	return ti, nil
}

func (ti *tableInfo) index(name string) int {
	return slices.IndexFunc(ti.cols, func(c colInfo) bool { return strings.EqualFold(c.name, name) })
}

// enumValues reads the values of enum('a','b”c') or set(...).
func enumValues(colType string) []string {
	i := strings.IndexByte(colType, '(')
	if i < 0 {
		return nil
	}
	s := colType[i+1:]
	var out []string
	for {
		if len(s) == 0 || s[0] != '\'' {
			return out
		}
		s = s[1:]
		var b strings.Builder
		for len(s) > 0 {
			if s[0] == '\'' {
				if len(s) > 1 && s[1] == '\'' {
					b.WriteByte('\'')
					s = s[2:]
					continue
				}
				s = s[1:]
				break
			}
			if s[0] == '\\' && len(s) > 1 {
				s = s[1:]
			}
			b.WriteByte(s[0])
			s = s[1:]
		}
		out = append(out, b.String())
		if len(s) == 0 || s[0] != ',' {
			return out
		}
		s = s[1:]
	}
}

// onTableMap completes a table map of a table read with what its optional
// metadata lacks, from information_schema, and works out its key's columns.
// An error is kept for the Decode that called it.
func (st *streamer) onTableMap(tm *binlog.TableMap) {
	ns := tm.Schema + "." + tm.Table
	if !st.s.included(ns) {
		delete(st.keys, tm.TableID)
		return
	}
	full := tm.Metadata&(1<<binlog.MetaColumnName) != 0
	var ti *tableInfo
	if !full {
		ti = st.tables[ns]
		if ti == nil {
			c, err := st.metaConn(st.runCtx)
			if err == nil {
				ti, err = readTable(st.runCtx, c, tm.Schema, tm.Table)
			}
			if err != nil {
				st.dropMeta()
				st.err = err
				return
			}
			if len(ti.cols) != len(tm.Columns) {
				st.s.log.Warnf("sync mysql: %s has %d columns in information_schema and %d in the binlog, which was written before a change of the table: "+
					"columns past the first %d are named @n by their position (binlog_row_metadata=FULL names them all)",
					ns, len(ti.cols), len(tm.Columns), min(len(ti.cols), len(tm.Columns)))
			}
			st.tables[ns] = ti
		}
		for i := range tm.Columns {
			c := &tm.Columns[i]
			if i >= len(ti.cols) {
				c.Name = "@" + strconv.Itoa(i+1)
				continue
			}
			ci := ti.cols[i]
			c.Name = ci.name
			if tm.Metadata&(1<<binlog.MetaSignedness) == 0 {
				c.Unsigned = ci.unsigned
			}
			if c.Charset == 0 {
				c.Charset = charsetText
				if ci.binary {
					c.Charset = binlog.CharsetBinary
				}
			}
			if c.EnumValues == nil && c.Type == binlog.TypeEnum {
				c.EnumValues = ci.enum
			}
			if c.SetValues == nil && c.Type == binlog.TypeSet {
				c.SetValues = ci.enum
			}
		}
	}
	var key []int
	if names, ok := st.s.Keys[ns]; ok {
		for _, n := range names {
			i := slices.IndexFunc(tm.Columns, func(c binlog.Column) bool { return strings.EqualFold(c.Name, n) })
			if i < 0 {
				st.err = fmt.Errorf("mysql: keys names column %q, which %s does not have", n, ns)
				return
			}
			key = append(key, i)
		}
	} else if full {
		key = tm.PrimaryKey
	} else {
		for _, i := range ti.pk {
			if i < len(tm.Columns) {
				key = append(key, i)
			}
		}
	}
	if key == nil {
		key = []int{}
	}
	st.keys[tm.TableID] = key
}

// identifier is a table's name in a DDL: quoted with backticks, or not.
const identifier = "(`(?:[^`]|``)+`|[^\\s.,;(`]+)"

var ddlTableRE = regexp.MustCompile(`(?is)^(?:CREATE|ALTER|DROP|TRUNCATE|RENAME)\s+(?:(?:TEMPORARY|ONLINE|OFFLINE|IGNORE)\s+)*` +
	`(?:TABLE|(?:UNIQUE\s+|FULLTEXT\s+|SPATIAL\s+)?INDEX\s+` + identifier + `\s+ON)\s+(?:IF\s+(?:NOT\s+)?EXISTS\s+)?` +
	identifier + `(?:\s*\.\s*` + identifier + `)?`)

// ddlTable returns the table a DDL changes, db.table, with db the
// statement's default database unless it names one; false when it is not
// a table's DDL.
func ddlTable(sql, db string) (string, bool) {
	m := ddlTableRE.FindStringSubmatch(strings.TrimSpace(sql))
	if m == nil {
		return "", false
	}
	a, b := unquoteIdent(m[2]), unquoteIdent(m[3])
	if m[3] == "" {
		if db == "" {
			return "", false
		}
		return db + "." + a, true
	}
	return a + "." + b, true
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '`' {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	return s
}
