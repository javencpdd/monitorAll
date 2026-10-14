package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— v3 MapBoard：节点持久化 —————————————————
//
// 设计约束（违反任意一条都会产生"不报错但行为错"的问题）：
//  1. 节点的结构性变更（增删节点、改绑定）与 dashboards.revision 的自增必须在【同一个事务】里完成。
//     分成两个事务时，第二个失败会让 revision 已经 +1 而节点没变（H16 同类问题）。
//  2. 禁止事务嵌套：SetMaxOpenConns(1) 下事务内再 BeginTx 是【挂住】而不是报错，最难排查（H18）。
//  3. 节点实时位置不落库，本文件只处理绑定关系与兜底坐标。

// mapNodeColumns 为 map_nodes 的显式列清单（禁止 SELECT *：加列会破坏 Scan 顺序）。
const mapNodeColumns = `id, board_id, channel_id, name, fallback_lat, fallback_lon, fallback_crs,
	payload_type, lat_path, lon_path, yaw_path, yaw_unit, static, created_at, updated_at`

// nullFloat64 把 *float64 转成可空列值。
//
// 不能把 nil 指针直接交给驱动：不同驱动的空值处理不一致，显式转换才能保证
// 无兜底坐标真的写成 NULL —— 这是"无兜底"语义的唯一载体（0 是合法坐标，不能当哨兵）。
func nullFloat64(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// scanMapNode 把一行映射为 model.MapNode。
func scanMapNode(rows *sql.Rows) (*model.MapNode, error) {
	var (
		n        model.MapNode
		chID     sql.NullString
		lat, lon sql.NullFloat64
		static   int
	)
	if err := rows.Scan(&n.ID, &n.BoardID, &chID, &n.Name, &lat, &lon, &n.FallbackCRS,
		&n.PayloadType, &n.LatPath, &n.LonPath, &n.YawPath, &n.YawUnit, &static,
		&n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "扫描地图节点行失败")
	}
	n.ChannelID = chID.String
	n.Static = static == 1
	if lat.Valid {
		v := lat.Float64
		n.FallbackLat = &v
	}
	if lon.Valid {
		v := lon.Float64
		n.FallbackLon = &v
	}
	return &n, nil
}

// ListMapNodes 列出某看板下的全部节点。
func (s *SQLite) ListMapNodes(boardID string) ([]model.MapNode, error) {
	rows, err := s.db.Query(`SELECT `+mapNodeColumns+` FROM map_nodes
		WHERE board_id = ? ORDER BY created_at ASC, id ASC`, boardID)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询地图节点失败")
	}
	defer rows.Close()
	out := make([]model.MapNode, 0)
	for rows.Next() {
		n, err := scanMapNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// GetMapNode 按 ID 读取节点。
func (s *SQLite) GetMapNode(id string) (*model.MapNode, error) {
	rows, err := s.db.Query(`SELECT `+mapNodeColumns+` FROM map_nodes WHERE id = ?`, id)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询地图节点失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询地图节点失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "map node %s not found", id)
	}
	return scanMapNode(rows)
}

// CountMapNodes 统计某看板下的节点数（规模校验用，上限 model.MaxNodesPerBoard）。
func (s *SQLite) CountMapNodes(boardID string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM map_nodes WHERE board_id = ?`, boardID).Scan(&n); err != nil {
		return 0, apperr.Wrap(err, apperr.StoreError, "统计节点数失败")
	}
	return n, nil
}

// MapNodeMutationResult 为节点结构性变更的结果（含自增后的 revision 与受影响的卡片数）。
type MapNodeMutationResult struct {
	Node     model.MapNode `json:"node"`
	Revision int64         `json:"revision"`
	Unbound  int           `json:"unbound"` // 解绑保留下来的卡片数
	Deleted  int           `json:"deleted"` // 一并删除的卡片数
}

// bumpMapBoardRevision 在【传入的事务内】对地图看板做 revision 的 CAS 自增。
//
// 用 SQL 层的 `WHERE revision = ?` 做比较并交换，而不是"先读再写"——
// 后者是 TOCTOU，并发下两个请求都能通过校验（H16 同类问题）。
func bumpMapBoardRevision(ctx context.Context, t *sql.Tx, boardID string, expect int64) (int64, error) {
	if expect <= 0 {
		return 0, apperr.New(apperr.InvalidParam, "revision 必填且必须大于 0")
	}
	now := model.NowMs()
	res, err := t.ExecContext(ctx,
		`UPDATE dashboards SET revision = revision + 1, updated_at = ?
		 WHERE id = ? AND kind = 'map' AND revision = ?`, now, boardID, expect)
	if err != nil {
		return 0, apperr.Wrap(err, apperr.StoreError, "自增看板 revision 失败")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
	}
	if n == 1 {
		return expect + 1, nil
	}
	// 区分"看板不存在 / 不是地图看板"与"revision 冲突"，两者错误信息必须不同。
	var kind string
	err = t.QueryRowContext(ctx, `SELECT kind FROM dashboards WHERE id = ?`, boardID).Scan(&kind)
	if err == sql.ErrNoRows {
		return 0, apperr.Newf(apperr.NotFound, "dashboard %s not found", boardID)
	}
	if err != nil {
		return 0, apperr.Wrap(err, apperr.StoreError, "校验看板失败")
	}
	if kind != string(model.BoardKindMap) {
		return 0, apperr.New(apperr.UnsupportedType, "该接口仅适用于地图看板（kind=map）")
	}
	return 0, apperr.New(apperr.RevisionConflict, "看板已被修改，请刷新后重试")
}

// CreateMapNode 在单个事务内落库节点并自增 revision。
func (s *SQLite) CreateMapNode(boardID string, n *model.MapNode, expectRevision int64) (*MapNodeMutationResult, error) {
	if n.ID == "" {
		n.ID = model.NewMapNodeID()
	}
	now := model.NowMs()
	if n.CreatedAt == 0 {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	n.BoardID = boardID
	if n.FallbackCRS == "" {
		n.FallbackCRS = model.DefaultCRS
	}
	if n.YawUnit == "" {
		n.YawUnit = "deg"
	}
	out := &MapNodeMutationResult{Node: *n}
	ctx := context.Background()
	err := s.tx(ctx, func(t *sql.Tx) error {
		// 规模上限在同一个事务内校验，避免"检查通过后再插入"的竞态。
		var cnt int
		if err := t.QueryRowContext(ctx,
			`SELECT count(*) FROM map_nodes WHERE board_id = ?`, boardID).Scan(&cnt); err != nil {
			return apperr.Wrap(err, apperr.StoreError, "统计节点数失败")
		}
		if cnt >= model.MaxNodesPerBoard {
			return apperr.Newf(apperr.InvalidParam,
				"单个地图看板的节点数上限为 %d，当前已有 %d 个", model.MaxNodesPerBoard, cnt)
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO map_nodes (`+mapNodeColumns+`)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ID, n.BoardID, nullString(n.ChannelID), n.Name,
			nullFloat64(n.FallbackLat), nullFloat64(n.FallbackLon),
			string(n.FallbackCRS), n.PayloadType, n.LatPath, n.LonPath, n.YawPath, n.YawUnit,
			boolToInt(n.Static), n.CreatedAt, n.UpdatedAt); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return apperr.Wrap(err, apperr.InvalidParam, "节点 ID 已存在")
			}
			return err
		}
		rev, err := bumpMapBoardRevision(ctx, t, boardID, expectRevision)
		if err != nil {
			return err
		}
		out.Revision = rev
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateMapNode 在单个事务内全量更新节点并自增 revision。
func (s *SQLite) UpdateMapNode(boardID string, n *model.MapNode, expectRevision int64) (*MapNodeMutationResult, error) {
	n.UpdatedAt = model.NowMs()
	n.BoardID = boardID
	if n.FallbackCRS == "" {
		n.FallbackCRS = model.DefaultCRS
	}
	if n.YawUnit == "" {
		n.YawUnit = "deg"
	}
	out := &MapNodeMutationResult{Node: *n}
	ctx := context.Background()
	err := s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `UPDATE map_nodes SET channel_id = ?, name = ?,
			fallback_lat = ?, fallback_lon = ?, fallback_crs = ?, payload_type = ?,
			lat_path = ?, lon_path = ?, yaw_path = ?, yaw_unit = ?, static = ?, updated_at = ?
			WHERE id = ? AND board_id = ?`,
			nullString(n.ChannelID), n.Name, n.FallbackLat, n.FallbackLon, string(n.FallbackCRS),
			n.PayloadType, n.LatPath, n.LonPath, n.YawPath, n.YawUnit, boolToInt(n.Static),
			n.UpdatedAt, n.ID, boardID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
		}
		if affected == 0 {
			return apperr.Newf(apperr.NotFound, "map node %s not found in board %s", n.ID, boardID)
		}
		rev, err := bumpMapBoardRevision(ctx, t, boardID, expectRevision)
		if err != nil {
			return err
		}
		out.Revision = rev
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteMapNode 删除节点；cascade=true 时一并删除其卡片，否则解绑保留（默认，C10）。
//
// 解绑保留的意义：卡片转"未锚定"后仍在看板里，用户可 rebind 到其它节点，
// 且 map_geom 被保留，重新绑定后位置回来了。删卡是不可恢复的，不能作为默认行为。
func (s *SQLite) DeleteMapNode(boardID, nodeID string, cascade bool, expectRevision int64) (*MapNodeMutationResult, error) {
	out := &MapNodeMutationResult{}
	ctx := context.Background()
	err := s.tx(ctx, func(t *sql.Tx) error {
		n, err := getMapNodeTx(ctx, t, boardID, nodeID)
		if err != nil {
			return err
		}
		out.Node = *n
		now := model.NowMs()
		if cascade {
			res, err := t.ExecContext(ctx,
				`DELETE FROM cards WHERE node_id = ? AND dashboard_id = ?`, nodeID, boardID)
			if err != nil {
				return err
			}
			del, err := res.RowsAffected()
			if err != nil {
				return apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
			}
			out.Deleted = int(del)
		} else {
			// 只清 node_id，不碰 anchor_mode / map_geom：
			// "未锚定"由 node_id = '' 唯一表达（写 anchor_mode='free' 会与 node_id 形成双真源，
			// 且在重新挂载时被永久继承）；map_geom 保留，重新绑定后位置能回来。
			res, err := t.ExecContext(ctx,
				`UPDATE cards SET node_id = '', updated_at = ?
				 WHERE node_id = ? AND dashboard_id = ?`, now, nodeID, boardID)
			if err != nil {
				return err
			}
			unbound, err := res.RowsAffected()
			if err != nil {
				return apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
			}
			out.Unbound = int(unbound)
		}
		if _, err := t.ExecContext(ctx,
			`DELETE FROM map_nodes WHERE id = ? AND board_id = ?`, nodeID, boardID); err != nil {
			return err
		}
		rev, err := bumpMapBoardRevision(ctx, t, boardID, expectRevision)
		if err != nil {
			return err
		}
		out.Revision = rev
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// getMapNodeTx 在事务内读取属于指定看板的节点（跨板访问一律 NotFound，不泄露存在性）。
func getMapNodeTx(ctx context.Context, t *sql.Tx, boardID, nodeID string) (*model.MapNode, error) {
	rows, err := t.QueryContext(ctx, `SELECT `+mapNodeColumns+`
		FROM map_nodes WHERE id = ? AND board_id = ?`, nodeID, boardID)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询地图节点失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询地图节点失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "map node %s not found", nodeID)
	}
	return scanMapNode(rows)
}

// noChannelSQL 是 map_nodes.channel_id 表达"无通道（纯静态节点）"时的落库值。
//
// ⚠️ 它与 schema.go 里 map_nodes.channel_id 的 DDL **强耦合，两种 DDL 互斥**，
// 不存在能同时满足两者的写法，改动必须与 DDL 同批落地：
//
//   - 当前已落地的 DDL 是 `channel_id TEXT`（可空）
//   - `FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE SET NULL`，
//     且 foreign_keys 恒为 ON（store.go DSN pragma + backup.go 启动校验）。
//     此时**只能用 NULL**：空串非 NULL 却匹配不到任何 channels.id，
//     会直接报 FOREIGN KEY constraint failed 让整个删通道事务回滚（已实测）。
//   - 若改为 `channel_id TEXT NOT NULL DEFAULT ”` 且**去掉外键**，则必须同步改成 `”`，
//     否则 NOT NULL 约束会让删通道事务回滚（同样致命）。
//
// 注意：两种表示在 Go 侧完全等价 —— scanMapNode 用 sql.NullString 读回，
// NULL 与 ” 都映射成 model.MapNode.ChannelID == ""。
const noChannelSQL = `NULL`

// DetachMapNodesByChannel 在【传入的事务内】处理"通道被删除"时的节点归属与卡片解绑：
//   - 卡片：解绑挂在【即将被删】节点上的卡片（保留节点上的卡片不动，用户无需重新挂载）。
//   - 节点：有兜底坐标（fallback_lat 非 NULL）→ 降级为静态节点保留；无兜底 → 随通道删除。
//
// 三步顺序不可交换：
//  0. 先取待解绑卡片 ID 并解绑 —— 卡片挂在节点上，节点删了就查不到谁还指着它；
//  1. 再转静态；
//  2. 最后删节点 —— 颠倒会让 cards.node_id 指向已不存在的节点（孤儿引用）。
//
// 返回保留节点数、删除节点数、被解绑的卡片 ID（供调用方广播 cards.changed 与打日志；
// 删通道是破坏性操作，必须留痕）。
func DetachMapNodesByChannel(ctx context.Context, t *sql.Tx, channelID string) (kept, removed int, unboundCards []string, err error) {
	now := model.NowMs()
	unboundCards = make([]string, 0)

	// 0a 取 ID：必须放在删除节点之前。
	const doomed = `SELECT id FROM map_nodes WHERE channel_id = ? AND fallback_lat IS NULL`
	rows, err := t.QueryContext(ctx,
		`SELECT id FROM cards WHERE node_id IN (`+doomed+`)`, channelID)
	if err != nil {
		return 0, 0, nil, apperr.Wrap(err, apperr.StoreError, "查询待解绑卡片失败")
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, nil, apperr.Wrap(err, apperr.StoreError, "读取待解绑卡片失败")
		}
		unboundCards = append(unboundCards, id)
	}
	// 必须先 Close 再 Exec：database/sql 的 Tx 在同一连接上同时只允许一个活跃语句，
	// 未关闭的 Rows 会让后续 Exec 一直等（单连接下表现为挂住而不是报错）。
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, nil, apperr.Wrap(err, apperr.StoreError, "读取待解绑卡片失败")
	}

	// 0b 解绑：只清 node_id，不动 anchor_mode / map_geom（理由见 DeleteMapNode）。
	if len(unboundCards) > 0 {
		if _, err := t.ExecContext(ctx,
			`UPDATE cards SET node_id = '', updated_at = ? WHERE node_id IN (`+doomed+`)`,
			now, channelID); err != nil {
			return 0, 0, nil, err
		}
	}

	// 1 有兜底 → 置空 channel_id 并转静态。
	res, err := t.ExecContext(ctx,
		`UPDATE map_nodes SET channel_id = `+noChannelSQL+`, static = 1, updated_at = ?
		 WHERE channel_id = ? AND fallback_lat IS NOT NULL`, now, channelID)
	if err != nil {
		return 0, 0, nil, err
	}
	k, err := res.RowsAffected()
	if err != nil {
		return 0, 0, nil, apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
	}
	// 2 无兜底 → 节点失去唯一位置来源，随通道删除
	res2, err := t.ExecContext(ctx,
		`DELETE FROM map_nodes WHERE channel_id = ? AND fallback_lat IS NULL`, channelID)
	if err != nil {
		return 0, 0, nil, err
	}
	r, err := res2.RowsAffected()
	if err != nil {
		return 0, 0, nil, apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
	}
	return int(k), int(r), unboundCards, nil
}

// GeometryResult 为轻量几何保存的结果。
type GeometryResult struct {
	Saved     int   `json:"saved"`
	Skipped   int   `json:"skipped"`
	UpdatedAt int64 `json:"updatedAt"`
}

// SaveCardGeometry 批量写入卡片在地图看板下的像素几何。
//
// 与看板结构保存的关键差异（C7）：
//   - 【不校验、不递增 dashboards.revision】。几何是"视图偏好"，最坏结果是重拖一次；
//     若纳入 revision 域，一次拖拽会产生数十次 revision 自增，制造 40009 冲突风暴。
//   - 整个批次一个事务（禁止逐卡开事务：既慢又会在单连接下抢锁）。
//   - 不更新 dashboards.updated_at，避免列表页被拖拽刷屏。
func (s *SQLite) SaveCardGeometry(boardID string, items map[string]*model.MapGeometry) (*GeometryResult, error) {
	if len(items) == 0 {
		return &GeometryResult{}, nil
	}
	now := model.NowMs()
	out := &GeometryResult{UpdatedAt: now}
	ctx := context.Background()
	err := s.tx(ctx, func(t *sql.Tx) error {
		stmt, err := t.PrepareContext(ctx,
			`UPDATE cards SET map_geom = ?, updated_at = ? WHERE id = ? AND dashboard_id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, g := range items {
			blob, err := marshalJSON(g)
			if err != nil {
				return err
			}
			res, err := stmt.ExecContext(ctx, blob, now, id, boardID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return apperr.Wrap(err, apperr.StoreError, "读取影响行数失败")
			}
			if n == 0 {
				// 卡片不属于本看板（可能是旧数据残留）：计入 skipped，不报错、不影响其它项。
				out.Skipped++
				continue
			}
			out.Saved++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DirtyNodeRef 为自检发现的一条脏引用（软引用无外键，只能靠启动自检兜底）。
type DirtyNodeRef struct {
	CardID  string `json:"cardId"`
	BoardID string `json:"boardId"`
	NodeID  string `json:"nodeId"`
	Problem string `json:"problem"` // node_missing | node_other_board | board_not_map
}

// FindDirtyNodeRefs 列出 cards.node_id 的脏引用：指向不存在的节点、或指向别的看板的节点。
//
// 这些数据不会让查询崩（查询侧一律当作"未锚定"处理），但会让用户看到"卡片莫名不见了"。
// 自检只是【报告】，不自动改数据 —— 自动修复属于破坏性操作，必须人工确认。
func (s *SQLite) FindDirtyNodeRefs() ([]DirtyNodeRef, error) {
	rows, err := s.db.Query(`SELECT c.id, c.dashboard_id, c.node_id,
		(SELECT n.board_id FROM map_nodes n WHERE n.id = c.node_id) AS node_board,
		(SELECT d.kind FROM dashboards d WHERE d.id = c.dashboard_id) AS board_kind
		FROM cards c WHERE c.node_id <> ''`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "自检节点引用失败")
	}
	defer rows.Close()
	out := make([]DirtyNodeRef, 0)
	for rows.Next() {
		var (
			cardID, boardID, nodeID string
			nodeBoard, boardKind    sql.NullString
		)
		if err := rows.Scan(&cardID, &boardID, &nodeID, &nodeBoard, &boardKind); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "扫描自检结果失败")
		}
		switch {
		case !nodeBoard.Valid:
			out = append(out, DirtyNodeRef{cardID, boardID, nodeID, "node_missing"})
		case nodeBoard.String != boardID:
			out = append(out, DirtyNodeRef{cardID, boardID, nodeID, "node_other_board"})
		case boardKind.Valid && boardKind.String != string(model.BoardKindMap):
			out = append(out, DirtyNodeRef{cardID, boardID, nodeID, "board_not_map"})
		}
	}
	return out, rows.Err()
}
