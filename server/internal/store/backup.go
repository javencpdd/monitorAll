package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monitorall/monitorall/internal/apperr"
)

// backupKeepCount 为迁移前自动备份的保留份数（保留最近的 N 份）。
const backupKeepCount = 3

// isMemoryDSN 判断 DSN 是否为内存库（内存库无备份意义）。
func isMemoryDSN(dsn string) bool {
	if dsn == "" || strings.Contains(dsn, ":memory:") {
		return true
	}
	return strings.HasPrefix(dsn, "file:") && strings.Contains(dsn, "mode=memory")
}

// dbFilePath 从 DSN 还原数据库文件路径；非文件型 DSN 返回空串。
func dbFilePath(dsn string) string {
	if isMemoryDSN(dsn) {
		return ""
	}
	p := dsn
	if strings.HasPrefix(p, "file:") {
		p = strings.TrimPrefix(p, "file:")
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p = p[:i]
		}
	}
	return p
}

// backupDir 返回备份目录（数据库文件同级的 backup/）。
func backupDir(dsn string) string {
	p := dbFilePath(dsn)
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "backup")
}

// snapshotTo 用 `VACUUM INTO` 产出一份一致性快照。
//
// 不用裸拷贝主库文件：后者会漏掉尚未 checkpoint 的 WAL 内容，得到的备份在迁移失败时
// 可能根本无法恢复。VACUUM INTO 产出的是完整、已整理的一致性副本，等价于 Online Backup API。
func (s *SQLite) snapshotTo(ctx context.Context, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", apperr.Wrapf(err, apperr.StoreError, "创建备份目录 %s 失败", dir)
	}
	dst := filepath.Join(dir, fmt.Sprintf("monitorall-%s.db", time.Now().Format("20060102-150405.000")))
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`VACUUM INTO %q`, dst)); err != nil {
		return "", apperr.Wrapf(err, apperr.StoreError, "生成数据库备份 %s 失败", dst)
	}
	if err := pruneBackups(dir, backupKeepCount); err != nil {
		// 清理失败不影响本次备份已生成，但要留出口（本项目铁律：静默失败不可接受）。
		return dst, apperr.Wrapf(err, apperr.StoreError, "清理历史备份失败（备份 %s 已生成）", dst)
	}
	return dst, nil
}

// backupBeforeMigrate 在执行升级迁移前做一份一致性快照。
//
// 仅在 current < target（即真的要升级）时执行；版本已是最新则跳过，避免每次启动都产生一份备份。
// 备份失败直接中止迁移 —— 迁移是不可逆的，没有备份就不允许升级（明确报错，不静默继续）。
func (s *SQLite) backupBeforeMigrate(ctx context.Context, current, target int) (string, error) {
	if current >= target {
		return "", nil
	}
	dbPath := dbFilePath(s.dsn)
	if dbPath == "" {
		return "", nil // 内存库（测试/临时实例）无需备份
	}
	if _, err := os.Stat(dbPath); err != nil {
		return "", nil // 库文件尚不存在（首次启动），无内容可备份
	}
	dst, err := s.snapshotTo(ctx, backupDir(s.dsn))
	if err != nil {
		return "", apperr.Wrapf(err, apperr.StoreError,
			"迁移 v%d→v%d 前的自动备份失败，已中止迁移（请先确保 %s 可写且磁盘有空间）",
			current, target, backupDir(s.dsn))
	}
	return dst, nil
}

// pruneBackups 保留最近 keep 份备份，删除更旧的（文件名含时间戳，字典序即时序）。
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "monitorall-") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) <= keep {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[keep:] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}

// ensurePragmas 无条件开启并校验外键约束。
//
// 历史问题（H10）：此前 `PRAGMA foreign_keys=ON` 只写在 store.wal=true 分支的 DSN 查询串里。
// 一旦部署把 wal 置为 false，v3 依赖的 map_nodes 两个外键（ON DELETE CASCADE / ON DELETE SET NULL）
// 会完全不生效且不报任何错 —— 表现为删看板留下孤儿节点、删通道留下脏引用，排查极其困难。
func (s *SQLite) ensurePragmas(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return apperr.Wrap(err, apperr.StoreError, "开启外键约束失败")
	}
	var fk int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return apperr.Wrap(err, apperr.StoreError, "校验外键约束状态失败")
	}
	if fk != 1 {
		return apperr.New(apperr.StoreError, "外键约束未能开启（PRAGMA foreign_keys=0），请检查 SQLite 构建与 DSN")
	}
	return nil
}
