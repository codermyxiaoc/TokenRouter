package setup

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// 已存在的目标库不应要求安装账号有 postgres 维护库权限。
func TestDatabaseConnectionPrefersConfiguredTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectClose()
	var opened []string
	err = testDatabaseConnection(&DatabaseConfig{DBName: "tokenrouter"}, func(_ *DatabaseConfig, name string) (*sql.DB, error) {
		opened = append(opened, name)
		return db, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"tokenrouter"}, opened)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 仅数据库不存在时使用维护库；存在性检查也覆盖并发建库已经完成的情况。
func TestDatabaseConnectionMissingTargetBootstrap(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_created_%t", exists), func(t *testing.T) {
			maintenance, mock, err := sqlmock.New()
			require.NoError(t, err)
			target, targetMock, err := sqlmock.New()
			require.NoError(t, err)
			mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).WithArgs("tokenrouter").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
			if !exists {
				mock.ExpectExec("CREATE DATABASE tokenrouter").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectClose()
			targetMock.ExpectClose()
			var opened []string
			err = testDatabaseConnection(&DatabaseConfig{DBName: "tokenrouter"}, func(_ *DatabaseConfig, name string) (*sql.DB, error) {
				opened = append(opened, name)
				switch len(opened) {
				case 1:
					return nil, fmt.Errorf("connect target: %w", &pq.Error{Code: "3D000"})
				case 2:
					return maintenance, nil
				default:
					return target, nil
				}
			})
			require.NoError(t, err)
			require.Equal(t, []string{"tokenrouter", "postgres", "tokenrouter"}, opened)
			require.NoError(t, mock.ExpectationsWereMet())
			require.NoError(t, targetMock.ExpectationsWereMet())
		})
	}
}

// 认证、权限、网络故障必须保留原始错误，不能尝试切库或建库。
func TestDatabaseConnectionDoesNotBootstrapOtherFailures(t *testing.T) {
	for _, failure := range []error{
		&pq.Error{Code: "28P01", Message: "authentication failed"},
		&pq.Error{Code: "42501", Message: "permission denied"},
		errors.New("network timeout"),
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			var opened []string
			err := testDatabaseConnection(&DatabaseConfig{DBName: "tokenrouter"}, func(_ *DatabaseConfig, name string) (*sql.DB, error) {
				opened = append(opened, name)
				return nil, failure
			})
			require.ErrorIs(t, err, failure)
			require.Equal(t, []string{"tokenrouter"}, opened)
		})
	}
}
