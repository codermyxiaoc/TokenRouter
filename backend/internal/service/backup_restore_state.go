package service

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

func (s *BackupService) beginBackupRestore(ctx context.Context, id string) (*BackupRecord, error) {
	ctx, release, err := s.lockBackupRecordUpdates(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	records, err := s.loadRecordsLocked(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ID != id {
			continue
		}
		if records[i].Status != "completed" {
			return nil, infraerrors.BadRequest("BACKUP_NOT_COMPLETED", "can only restore from a completed backup")
		}
		if records[i].RestoreStatus == "running" {
			return nil, ErrRestoreInProgress
		}
		records[i].RestoreStatus = "running"
		records[i].RestoreStartedAt = time.Now().Format(time.RFC3339)
		records[i].RestoreError = ""
		if err := s.saveRecordsLocked(ctx, records); err != nil {
			return nil, err
		}
		return &records[i], nil
	}
	return nil, ErrBackupNotFound
}

// 失败或异常退出的恢复不能复活已删除记录。成功的全库恢复可能回滚记录本身，
// 因此仅成功完成时沿用显式补回当前备份记录的行为。
func (s *BackupService) saveRestoreRecord(ctx context.Context, record *BackupRecord) error {
	ctx, release, err := s.lockBackupRecordUpdates(ctx)
	if err != nil {
		return err
	}
	defer release()
	records, err := s.loadRecordsLocked(ctx)
	if err != nil {
		return err
	}
	for i := range records {
		if records[i].ID != record.ID {
			continue
		}
		if records[i].RestoreStartedAt != record.RestoreStartedAt &&
			(record.RestoreStatus != "completed" || (records[i].RestoreStatus == "running" && records[i].RestoreStartedAt != "")) {
			return infraerrors.Conflict("BACKUP_RESTORE_STATE_CHANGED", "a newer restore operation owns this backup")
		}
		if record.RestoreStatus == "completed" {
			// 全库恢复可能让记录回退到上传前，补回完成快照时保留数据库已有归档保护。
			snapshot := *record
			if records[i].MonthlyArchive != nil {
				snapshot.MonthlyArchive = records[i].MonthlyArchive
				snapshot.ExpiresAt = ""
			}
			records[i] = snapshot
			return s.saveRecordsLocked(ctx, records)
		}
		// 仅更新本次恢复字段，保留归档元数据及其他写入方已记录的进度。
		records[i].RestoreStatus = record.RestoreStatus
		records[i].RestoreError = record.RestoreError
		records[i].RestoredAt = record.RestoredAt
		return s.saveRecordsLocked(ctx, records)
	}
	if record.RestoreStatus == "completed" {
		return s.saveRecordsLocked(ctx, append(records, *record))
	}
	return ErrBackupNotFound
}
