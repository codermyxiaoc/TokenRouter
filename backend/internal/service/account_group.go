package service

import "time"

type AccountGroup struct {
	AccountID int64
	GroupID   int64
	CreatedAt time.Time

	Account *Account
	Group   *Group
}
