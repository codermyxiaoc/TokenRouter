package repository

import (
	"context"
	"database/sql/driver"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/servertiming"
)

const fakeDriverDelay = 2 * time.Millisecond

type timingFakeDriver struct{}

func (timingFakeDriver) Open(string) (driver.Conn, error) { return newTimingFakeConn(), nil }

type timingFakeConnector struct {
	conn driver.Conn
}

func (c timingFakeConnector) Connect(context.Context) (driver.Conn, error) {
	time.Sleep(fakeDriverDelay)
	return c.conn, nil
}

func (timingFakeConnector) Driver() driver.Driver { return timingFakeDriver{} }

type timingFakeConn struct{}

func newTimingFakeConn() *timingFakeConn { return &timingFakeConn{} }

func (c *timingFakeConn) Prepare(string) (driver.Stmt, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeStmt{}, nil
}

func (c *timingFakeConn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeStmt{}, nil
}

func (c *timingFakeConn) Close() error { return nil }

func (c *timingFakeConn) Begin() (driver.Tx, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeTx{}, nil
}

func (c *timingFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeTx{}, nil
}

func (c *timingFakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	time.Sleep(fakeDriverDelay)
	return driver.RowsAffected(1), nil
}

func (c *timingFakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeRows{values: [][]driver.Value{{"value"}}}, nil
}

func (c *timingFakeConn) Ping(context.Context) error {
	time.Sleep(fakeDriverDelay)
	return nil
}

func (c *timingFakeConn) ResetSession(context.Context) error {
	time.Sleep(fakeDriverDelay)
	return nil
}

type timingFakeStmt struct{}

func (s *timingFakeStmt) Close() error  { return nil }
func (s *timingFakeStmt) NumInput() int { return -1 }

func (s *timingFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	time.Sleep(fakeDriverDelay)
	return driver.RowsAffected(1), nil
}

func (s *timingFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeRows{values: [][]driver.Value{{"value"}}}, nil
}

func (s *timingFakeStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	time.Sleep(fakeDriverDelay)
	return driver.RowsAffected(1), nil
}

func (s *timingFakeStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeRows{values: [][]driver.Value{{"value"}}}, nil
}

type timingFakeRows struct {
	values [][]driver.Value
	index  int
}

func (r *timingFakeRows) Columns() []string { return []string{"value"} }

func (r *timingFakeRows) Close() error {
	time.Sleep(fakeDriverDelay)
	return nil
}

func (r *timingFakeRows) Next(dest []driver.Value) error {
	time.Sleep(fakeDriverDelay)
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

type timingFakeTx struct{}

func (t *timingFakeTx) Commit() error {
	time.Sleep(fakeDriverDelay)
	return nil
}

func (t *timingFakeTx) Rollback() error {
	time.Sleep(fakeDriverDelay)
	return nil
}

func metricDuration(t *testing.T, header, metric string) float64 {
	t.Helper()
	re := regexp.MustCompile(`(?:^|, )` + regexp.QuoteMeta(metric) + `;dur=([0-9]+(?:\.[0-9]+)?)`)
	match := re.FindStringSubmatch(header)
	if len(match) != 2 {
		t.Fatalf("metric %q missing from header %q", metric, header)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("parse %s duration: %v", metric, err)
	}
	return value
}

func TestServerTimingConnectorRecordsDriverCallsWithoutRowLifetime(t *testing.T) {
	startedAt := time.Now()
	collector := servertiming.New(startedAt)
	ctx := servertiming.WithCollector(context.Background(), collector)

	wrapped := newServerTimingConnector(timingFakeConnector{conn: newTimingFakeConn()})
	// 外层计时包住驱动及采集开销，作为 DB 区间总和的实际可观测上界。
	// 不假设 Sleep 的实际时长或应用耗时与数据库耗时的大小关系。
	var driverElapsed time.Duration
	callStartedAt := time.Now()
	rawConn, err := wrapped.Connect(ctx)
	driverElapsed += time.Since(callStartedAt)
	if err != nil {
		t.Fatal(err)
	}
	conn, ok := rawConn.(*serverTimingConn)
	if !ok {
		t.Fatalf("Connect() returned %T, want *serverTimingConn", rawConn)
	}

	callStartedAt = time.Now()
	_, err = conn.ExecContext(ctx, "sensitive update", nil)
	driverElapsed += time.Since(callStartedAt)
	if err != nil {
		t.Fatal(err)
	}
	callStartedAt = time.Now()
	rows, err := conn.QueryContext(ctx, "sensitive select", nil)
	driverElapsed += time.Since(callStartedAt)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]driver.Value, 1)
	callStartedAt = time.Now()
	err = rows.Next(values)
	driverElapsed += time.Since(callStartedAt)
	if err != nil {
		t.Fatal(err)
	}

	// 两次读取行之间的应用处理必须继续计入应用耗时。
	processingStartedAt := time.Now()
	time.Sleep(30 * time.Millisecond)
	processingElapsed := time.Since(processingStartedAt)
	callStartedAt = time.Now()
	err = rows.Next(values)
	driverElapsed += time.Since(callStartedAt)
	if err != io.EOF {
		t.Fatalf("rows.Next() = %v, want EOF", err)
	}
	callStartedAt = time.Now()
	err = rows.Close()
	driverElapsed += time.Since(callStartedAt)
	if err != nil {
		t.Fatal(err)
	}

	header := collector.HeaderValue(time.Now(), "bypass")
	if !strings.Contains(header, `queries=2`) {
		t.Fatalf("header %q does not report two SQL operations", header)
	}
	if strings.Contains(header, "sensitive") {
		t.Fatalf("SQL text leaked into header: %q", header)
	}
	app, db := metricDuration(t, header, "app"), metricDuration(t, header, "db")
	// Header 只保留一位毫秒小数；容差仅覆盖其舍入，不掩盖操作间的应用间隔。
	const roundingToleranceMS = 0.1
	driverMS := float64(driverElapsed) / float64(time.Millisecond)
	processingMS := float64(processingElapsed) / float64(time.Millisecond)
	if db <= 0 || db > driverMS+roundingToleranceMS {
		t.Fatalf("DB time exceeds measured driver calls: db=%.1fms calls=%.3fms header=%q", db, driverMS, header)
	}
	if app+roundingToleranceMS < processingMS {
		t.Fatalf("row processing gap was not retained as application time: app=%.1fms gap=%.3fms header=%q", app, processingMS, header)
	}
}

func TestServerTimingPreparedStatementsAndTransactions(t *testing.T) {
	collector := servertiming.New(time.Now())
	ctx := servertiming.WithCollector(context.Background(), collector)
	conn := &serverTimingConn{Conn: newTimingFakeConn()}

	stmt, err := conn.PrepareContext(ctx, "prepare sensitive statement")
	if err != nil {
		t.Fatal(err)
	}
	timedStmt, ok := stmt.(*serverTimingStmt)
	if !ok {
		t.Fatalf("PrepareContext() returned %T, want *serverTimingStmt", stmt)
	}
	if _, err := timedStmt.ExecContext(ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := timedStmt.QueryContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	tx, err := conn.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := conn.ResetSession(ctx); err != nil {
		t.Fatal(err)
	}

	header := collector.HeaderValue(time.Now(), "bypass")
	if !strings.Contains(header, `queries=3`) {
		t.Fatalf("header %q does not report prepare, exec, and query operations", header)
	}
	if metricDuration(t, header, "db") <= 0 {
		t.Fatalf("DB duration was not recorded: %q", header)
	}
}

func TestNamedValuesRejectNamedParameters(t *testing.T) {
	if _, err := namedValues([]driver.NamedValue{{Name: "secret", Value: 1}}); err == nil {
		t.Fatal("namedValues accepted a named parameter")
	}
	values, err := namedValues([]driver.NamedValue{{Ordinal: 1, Value: "value"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != "value" {
		t.Fatalf("namedValues() = %#v", values)
	}
}
