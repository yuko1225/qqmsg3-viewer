package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite connection and provides query helpers.
type DB struct {
	conn   *sql.DB
	mu     sync.RWMutex
	tables []TableInfo // cached table list
}

// TableInfo describes a discovered chat table.
type TableInfo struct {
	Name    string `json:"name"`    // raw table name, e.g. "group_2013888236" or "buddy_12345678"
	Kind    string `json:"kind"`    // "group" or "buddy"
	ID      string `json:"id"`      // group number suffix or buddy QQ number
	Display string `json:"display"` // human-readable label
}

// Message represents a single chat message row.
type Message struct {
	Time       int64  `json:"time"`
	Rand       int64  `json:"rand"`
	SenderUin  uint64 `json:"sender_uin"`
	DecodedMsg string `json:"decoded_msg"`
}

// PageResult holds a page of messages plus navigation metadata.
type PageResult struct {
	Messages   []Message `json:"messages"`
	Total      int64     `json:"total"`
	HasPrev    bool      `json:"has_prev"`
	HasNext    bool      `json:"has_next"`
	FirstTime  int64     `json:"first_time"`
	LastTime   int64     `json:"last_time"`
	Offset     int       `json:"offset"` // actual offset used (may differ from requested when anchorTime is set)
}

var (
	groupRe = regexp.MustCompile(`^group_(\d+)$`)
	buddyRe = regexp.MustCompile(`^buddy_(\d+)$`)
)

// Open opens the SQLite database in read-only mode.
func Open(path string) (*DB, error) {
	// Use URI with mode=ro for read-only access
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL&_busy_timeout=5000", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	d := &DB{conn: conn}
	if err := d.loadTables(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// loadTables scans sqlite_master for chat tables.
func (d *DB) loadTables() error {
	rows, err := d.conn.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if m := groupRe.FindStringSubmatch(name); m != nil {
			tables = append(tables, TableInfo{
				Name:    name,
				Kind:    "group",
				ID:      m[1],
				Display: "群 " + m[1],
			})
		} else if m := buddyRe.FindStringSubmatch(name); m != nil {
			tables = append(tables, TableInfo{
				Name:    name,
				Kind:    "buddy",
				ID:      m[1],
				Display: "好友 " + m[1],
			})
		}
	}
	d.mu.Lock()
	d.tables = tables
	d.mu.Unlock()
	return nil
}

// Tables returns the list of discovered chat tables.
func (d *DB) Tables() []TableInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]TableInfo, len(d.tables))
	copy(result, d.tables)
	return result
}

// FindTable looks up a table by its raw name.
func (d *DB) FindTable(name string) (TableInfo, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, t := range d.tables {
		if t.Name == name {
			return t, true
		}
	}
	return TableInfo{}, false
}

// CountMessages returns total message count for a table, optionally filtered by keyword.
func (d *DB) CountMessages(table, keyword string) (int64, error) {
	var q string
	var args []interface{}
	if keyword != "" {
		q = fmt.Sprintf(`SELECT COUNT(*) FROM %q WHERE DecodedMsg LIKE ?`, table)
		args = append(args, "%"+keyword+"%")
	} else {
		q = fmt.Sprintf(`SELECT COUNT(*) FROM %q`, table)
	}
	var count int64
	err := d.conn.QueryRow(q, args...).Scan(&count)
	return count, err
}

// QueryMessages fetches a page of messages from a table.
// anchorTime: if > 0, fetch messages around this Unix timestamp (center page on it).
// pageSize: number of messages per page.
// offset: row offset (0-based).
// keyword: optional search string.
// senderUin: if > 0, filter by sender.
func (d *DB) QueryMessages(table string, offset, pageSize int, keyword string, senderUins []uint64, anchorTime int64) (PageResult, error) {
	var conditions []string
	var args []interface{}

	if keyword != "" {
		conditions = append(conditions, "DecodedMsg LIKE ?")
		args = append(args, "%"+keyword+"%")
	}
	if len(senderUins) == 1 {
		conditions = append(conditions, "SenderUin = ?")
		args = append(args, senderUins[0])
	} else if len(senderUins) > 1 {
		placeholders := strings.Repeat("?,", len(senderUins))
		placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
		conditions = append(conditions, "SenderUin IN ("+placeholders+")")
		for _, u := range senderUins {
			args = append(args, u)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM %q %s`, table, where)
	var total int64
	if err := d.conn.QueryRow(countQ, args...).Scan(&total); err != nil {
		return PageResult{}, fmt.Errorf("count: %w", err)
	}

	// If anchorTime is set, compute offset to center around that time
	if anchorTime > 0 {
		var anchorOffset int64
		// Count rows before anchorTime
		anchorArgs := append(args, anchorTime)
		anchorWhere := where
		if anchorWhere == "" {
			anchorWhere = "WHERE Time < ?"
		} else {
			anchorWhere += " AND Time < ?"
		}
		anchorQ := fmt.Sprintf(`SELECT COUNT(*) FROM %q %s`, table, anchorWhere)
		if err := d.conn.QueryRow(anchorQ, anchorArgs...).Scan(&anchorOffset); err == nil {
			// Place anchor in the middle of the page
			offset = int(anchorOffset) - pageSize/2
			if offset < 0 {
				offset = 0
			}
		}
	}

	if offset < 0 {
		offset = 0
	}

	// Fetch rows
	fetchArgs := append(args, pageSize, offset)
	q := fmt.Sprintf(`SELECT Time, Rand, SenderUin, COALESCE(DecodedMsg,'') FROM %q %s ORDER BY Time ASC, Rand ASC LIMIT ? OFFSET ?`, table, where)
	rows, err := d.conn.Query(q, fetchArgs...)
	if err != nil {
		return PageResult{}, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Time, &m.Rand, &m.SenderUin, &m.DecodedMsg); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}

	result := PageResult{
		Messages: msgs,
		Total:    total,
		HasPrev:  offset > 0,
		HasNext:  int64(offset+pageSize) < total,
		Offset:   offset, // return the actual offset used
	}
	if len(msgs) > 0 {
		result.FirstTime = msgs[0].Time
		result.LastTime = msgs[len(msgs)-1].Time
	}
	return result, nil
}

// GetSenders returns distinct SenderUin values in a table (for filter UI).
func (d *DB) GetSenders(table string) ([]uint64, error) {
	q := fmt.Sprintf(`SELECT DISTINCT SenderUin FROM %q ORDER BY SenderUin`, table)
	rows, err := d.conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uins []uint64
	for rows.Next() {
		var u uint64
		if err := rows.Scan(&u); err != nil {
			continue
		}
		uins = append(uins, u)
	}
	return uins, nil
}

// GetTimeRange returns the min and max Time for a table.
func (d *DB) GetTimeRange(table string) (minT, maxT int64, err error) {
	q := fmt.Sprintf(`SELECT MIN(Time), MAX(Time) FROM %q`, table)
	err = d.conn.QueryRow(q).Scan(&minT, &maxT)
	return
}

// ExportQuery holds parameters for a bulk export query.
type ExportQuery struct {
	Table      string
	Keyword    string
	SenderUins []uint64
	TimeFrom   int64 // 0 = no lower bound
	TimeTo     int64 // 0 = no upper bound
}

// QueryExport fetches a page of messages for export with time-range support.
// offset and limit control pagination; total is the count of all matching rows.
func (d *DB) QueryExport(q ExportQuery, offset, limit int) (msgs []Message, total int64, err error) {
	var conditions []string
	var args []interface{}

	if q.Keyword != "" {
		conditions = append(conditions, "DecodedMsg LIKE ?")
		args = append(args, "%"+q.Keyword+"%")
	}
	if len(q.SenderUins) == 1 {
		conditions = append(conditions, "SenderUin = ?")
		args = append(args, q.SenderUins[0])
	} else if len(q.SenderUins) > 1 {
		placeholders := strings.Repeat("?,", len(q.SenderUins))
		placeholders = placeholders[:len(placeholders)-1]
		conditions = append(conditions, "SenderUin IN ("+placeholders+")")
		for _, u := range q.SenderUins {
			args = append(args, u)
		}
	}
	if q.TimeFrom > 0 {
		conditions = append(conditions, "Time >= ?")
		args = append(args, q.TimeFrom)
	}
	if q.TimeTo > 0 {
		conditions = append(conditions, "Time <= ?")
		args = append(args, q.TimeTo)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %q %s`, q.Table, where)
	if err = d.conn.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("export count: %w", err)
	}

	fetchArgs := append(args, limit, offset)
	fetchSQL := fmt.Sprintf(`SELECT Time, Rand, SenderUin, COALESCE(DecodedMsg,'') FROM %q %s ORDER BY Time ASC, Rand ASC LIMIT ? OFFSET ?`, q.Table, where)
	rows, err := d.conn.Query(fetchSQL, fetchArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("export query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Time, &m.Rand, &m.SenderUin, &m.DecodedMsg); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, total, nil
}
