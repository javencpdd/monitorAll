package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/crypto"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— DataSource CRUD —————————————————

// scanDataSource 把一行映射为 model.DataSource（conn_params 中敏感键已是 ***）。
func scanDataSource(rows *sql.Rows) (*model.DataSource, error) {
	var (
		ds         model.DataSource
		connParams string
		secretKeys string
		retryJSON  sql.NullString
		enabled    int
	)
	err := rows.Scan(
		&ds.ID, &ds.Name, &ds.Kind, &ds.Protocol, &connParams, &secretKeys, &retryJSON,
		&ds.Status, &ds.LastError, &ds.LastOKAt, &enabled, &ds.CreatedAt, &ds.UpdatedAt, &ds.SchemaVersion,
	)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "扫描数据源行失败")
	}
	ds.Enabled = enabled != 0
	ds.ConnParams = map[string]any{}
	if err := unmarshalJSON(connParams, &ds.ConnParams); err != nil {
		return nil, err
	}
	ds.SecretKeys = []string{}
	if err := unmarshalJSON(secretKeys, &ds.SecretKeys); err != nil {
		return nil, err
	}
	if retryJSON.Valid && retryJSON.String != "" {
		rp := model.RetryPolicy{}
		if err := unmarshalJSON(retryJSON.String, &rp); err != nil {
			return nil, err
		}
		ds.RetryPolicy = &rp
	}
	return &ds, nil
}

// ListDataSources 返回全部数据源（运行态 status 由内存态覆盖，此处为库内值）。
func (s *SQLite) ListDataSources() ([]model.DataSource, error) {
	rows, err := s.db.Query(`SELECT id, name, kind, protocol, conn_params, secret_keys, retry_policy,
		status, last_error, last_ok_at, enabled, created_at, updated_at, schema_version
		FROM data_sources ORDER BY created_at ASC`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询数据源失败")
	}
	defer rows.Close()
	out := make([]model.DataSource, 0)
	for rows.Next() {
		ds, err := scanDataSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

// GetDataSource 按 ID 读取数据源，不存在返回 apperr.NotFound。
func (s *SQLite) GetDataSource(id string) (*model.DataSource, error) {
	rows, err := s.db.Query(`SELECT id, name, kind, protocol, conn_params, secret_keys, retry_policy,
		status, last_error, last_ok_at, enabled, created_at, updated_at, schema_version
		FROM data_sources WHERE id = ?`, id)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询数据源失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询数据源失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "data source %s not found", id)
	}
	ds, err := scanDataSource(rows)
	if err != nil {
		return nil, err
	}
	return ds, nil
}

// CreateDataSource 落库一个数据源：敏感字段加密进 secret_meta，conn_params 只留 ***。
func (s *SQLite) CreateDataSource(ds *model.DataSource) error {
	if ds.ID == "" {
		ds.ID = model.NewDataSourceID()
	}
	ds.SecretKeys = crypto.MergeSecretKeys(ds.SecretKeys, ds.Kind)
	now := model.NowMs()
	if ds.CreatedAt == 0 {
		ds.CreatedAt = now
	}
	ds.UpdatedAt = now
	if ds.SchemaVersion == 0 {
		ds.SchemaVersion = model.CurrentSchemaVersionForEntity
	}

	plain := crypto.CollectSecrets(ds.ConnParams, ds.SecretKeys)
	stored := crypto.MaskConnParams(ds.ConnParams, ds.SecretKeys)

	connJSON, err := marshalJSON(stored)
	if err != nil {
		return err
	}
	keysJSON, err := marshalJSON(ds.SecretKeys)
	if err != nil {
		return err
	}
	var retryJSON sql.NullString
	if ds.RetryPolicy != nil {
		v, err := marshalJSON(ds.RetryPolicy)
		if err != nil {
			return err
		}
		retryJSON = sql.NullString{String: v, Valid: true}
	}

	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `INSERT INTO data_sources
			(id, name, kind, protocol, conn_params, secret_keys, retry_policy,
			 status, last_error, last_ok_at, enabled, created_at, updated_at, schema_version)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ds.ID, ds.Name, string(ds.Kind), string(ds.Protocol), connJSON, keysJSON, retryJSON,
			string(ds.Status), ds.LastError, ds.LastOKAt, boolToInt(ds.Enabled),
			ds.CreatedAt, ds.UpdatedAt, ds.SchemaVersion); err != nil {
			return err
		}
		if err := s.saveSecrets(ctx, t, ds.ID, plain); err != nil {
			return err
		}
		return nil
	})
}

// UpdateDataSource 更新数据源；敏感值为 *** 时保持原值不变。
func (s *SQLite) UpdateDataSource(ds *model.DataSource) error {
	old, err := s.GetDataSource(ds.ID)
	if err != nil {
		return err
	}
	// 取出库中真实敏感值，回填 *** 后再统一重新加密
	secrets, err := s.loadSecrets(ds.ID)
	if err != nil {
		return err
	}
	realParams := crypto.ApplySecrets(old.ConnParams, secrets)
	merged := crypto.UnmaskConnParams(realParams, ds.ConnParams, ds.SecretKeys)

	ds.SecretKeys = crypto.MergeSecretKeys(ds.SecretKeys, ds.Kind)
	plain := crypto.CollectSecrets(merged, ds.SecretKeys)
	stored := crypto.MaskConnParams(merged, ds.SecretKeys)
	ds.ConnParams = stored

	connJSON, err := marshalJSON(stored)
	if err != nil {
		return err
	}
	keysJSON, err := marshalJSON(ds.SecretKeys)
	if err != nil {
		return err
	}
	var retryJSON sql.NullString
	if ds.RetryPolicy != nil {
		v, err := marshalJSON(ds.RetryPolicy)
		if err != nil {
			return err
		}
		retryJSON = sql.NullString{String: v, Valid: true}
	}
	ds.UpdatedAt = model.NowMs()

	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `UPDATE data_sources SET
			name = ?, kind = ?, protocol = ?, conn_params = ?, secret_keys = ?, retry_policy = ?,
			status = ?, last_error = ?, last_ok_at = ?, enabled = ?, updated_at = ?, schema_version = ?
			WHERE id = ?`,
			ds.Name, string(ds.Kind), string(ds.Protocol), connJSON, keysJSON, retryJSON,
			string(ds.Status), ds.LastError, ds.LastOKAt, boolToInt(ds.Enabled),
			ds.UpdatedAt, ds.SchemaVersion, ds.ID); err != nil {
			return err
		}
		if err := s.saveSecrets(ctx, t, ds.ID, plain); err != nil {
			return err
		}
		return nil
	})
}

// DeleteDataSource 删除数据源，并级联删除其通道、相关卡片与敏感字段。
func (s *SQLite) DeleteDataSource(id string) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		rows, err := t.QueryContext(ctx, `SELECT id FROM channels WHERE data_source_id = ?`, id)
		if err != nil {
			return err
		}
		chIDs := make([]string, 0)
		for rows.Next() {
			var chID string
			if err := rows.Scan(&chID); err != nil {
				rows.Close()
				return err
			}
			chIDs = append(chIDs, chID)
		}
		rows.Close()
		for _, chID := range chIDs {
			if _, err := t.ExecContext(ctx, `DELETE FROM cards WHERE channel_id = ?`, chID); err != nil {
				return err
			}
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM channels WHERE data_source_id = ?`, id); err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM data_sources WHERE id = ?`, id); err != nil {
			return err
		}
		return s.deleteSecrets(ctx, t, id)
	})
}

// ConnParamsFor 返回解密后的真实连接参数（仅适配器内部使用）。
func (s *SQLite) ConnParamsFor(id string) (map[string]any, error) {
	ds, err := s.GetDataSource(id)
	if err != nil {
		return nil, err
	}
	if len(ds.SecretKeys) == 0 {
		return ds.ConnParams, nil
	}
	secrets, err := s.loadSecrets(id)
	if err != nil {
		return nil, err
	}
	return crypto.ApplySecrets(ds.ConnParams, secrets), nil
}

// ————————————————— Channel CRUD —————————————————

// scanChannel 把一行映射为 model.Channel。
func scanChannel(rows *sql.Rows) (*model.Channel, error) {
	var (
		ch   model.Channel
		meta sql.NullString
	)
	if err := rows.Scan(&ch.ID, &ch.DataSourceID, &ch.Name, &ch.PayloadType, &meta,
		&ch.RateLimitHz, &ch.CreatedAt); err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "扫描通道行失败")
	}
	if meta.Valid && meta.String != "" {
		ch.Meta = map[string]any{}
		if err := unmarshalJSON(meta.String, &ch.Meta); err != nil {
			return nil, err
		}
	}
	return &ch, nil
}

// ListChannelsBySource 列出某数据源下的全部通道。
func (s *SQLite) ListChannelsBySource(dataSourceID string) ([]model.Channel, error) {
	rows, err := s.db.Query(`SELECT id, data_source_id, name, payload_type, meta, rate_limit_hz, created_at
		FROM channels WHERE data_source_id = ? ORDER BY created_at ASC`, dataSourceID)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询通道失败")
	}
	defer rows.Close()
	out := make([]model.Channel, 0)
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, rows.Err()
}

// ListAllChannels 列出全部通道（启动时恢复订阅用）。
func (s *SQLite) ListAllChannels() ([]model.Channel, error) {
	rows, err := s.db.Query(`SELECT id, data_source_id, name, payload_type, meta, rate_limit_hz, created_at
		FROM channels ORDER BY created_at ASC`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询通道失败")
	}
	defer rows.Close()
	out := make([]model.Channel, 0)
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, rows.Err()
}

// GetChannel 按 ID 读取通道，不存在返回 apperr.NotFound。
func (s *SQLite) GetChannel(id string) (*model.Channel, error) {
	rows, err := s.db.Query(`SELECT id, data_source_id, name, payload_type, meta, rate_limit_hz, created_at
		FROM channels WHERE id = ?`, id)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询通道失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询通道失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "channel %s not found", id)
	}
	return scanChannel(rows)
}

// CreateChannel 落库一个通道；同名通道（同数据源）直接命中唯一约束错误。
func (s *SQLite) CreateChannel(ch *model.Channel) error {
	if ch.ID == "" {
		ch.ID = model.NewChannelID()
	}
	if ch.CreatedAt == 0 {
		ch.CreatedAt = model.NowMs()
	}
	metaJSON, err := marshalJSON(ch.Meta)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `INSERT INTO channels
			(id, data_source_id, name, payload_type, meta, rate_limit_hz, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			ch.ID, ch.DataSourceID, ch.Name, string(ch.PayloadType), nullString(metaJSON), ch.RateLimitHz, ch.CreatedAt)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return apperr.Wrap(err, apperr.InvalidParam, "同名通道已存在")
			}
			return err
		}
		return nil
	})
}

// UpdateChannel 更新通道的 payloadType / meta / rateLimitHz。
func (s *SQLite) UpdateChannel(ch *model.Channel) error {
	metaJSON, err := marshalJSON(ch.Meta)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `UPDATE channels SET payload_type = ?, meta = ?, rate_limit_hz = ? WHERE id = ?`,
			string(ch.PayloadType), nullString(metaJSON), ch.RateLimitHz, ch.ID)
		return err
	})
}

// DeleteChannel 删除通道及其关联卡片。
func (s *SQLite) DeleteChannel(id string) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `DELETE FROM cards WHERE channel_id = ?`, id); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
		return err
	})
}

// DeleteChannelsBySource 删除某数据源下的全部通道与相关卡片。
func (s *SQLite) DeleteChannelsBySource(dataSourceID string) error {
	chs, err := s.ListChannelsBySource(dataSourceID)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		for _, ch := range chs {
			if _, err := t.ExecContext(ctx, `DELETE FROM cards WHERE channel_id = ?`, ch.ID); err != nil {
				return err
			}
		}
		_, err := t.ExecContext(ctx, `DELETE FROM channels WHERE data_source_id = ?`, dataSourceID)
		return err
	})
}

// ————————————————— Dashboard CRUD —————————————————

// scanDashboard 把一行映射为 model.Dashboard。
func scanDashboard(rows *sql.Rows) (*model.Dashboard, error) {
	var (
		d      model.Dashboard
		margin string
		global string
	)
	if err := rows.Scan(&d.ID, &d.Name, &d.Revision, &d.SchemaVersion, &d.GridCols, &d.RowHeight,
		&margin, &global, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "扫描看板行失败")
	}
	d.Margin = [2]int{model.DefaultMarginX, model.DefaultMarginY}
	if err := unmarshalJSON(margin, &d.Margin); err != nil {
		return nil, err
	}
	d.GlobalConfig = map[string]any{}
	if err := unmarshalJSON(global, &d.GlobalConfig); err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDashboards 列出全部看板（不含 cards）。
func (s *SQLite) ListDashboards() ([]model.Dashboard, error) {
	rows, err := s.db.Query(`SELECT id, name, revision, schema_version, grid_cols, row_height,
		margin, global_config, created_at, updated_at FROM dashboards ORDER BY created_at ASC`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询看板失败")
	}
	defer rows.Close()
	out := make([]model.Dashboard, 0)
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// GetDashboard 按 ID 读取看板。
func (s *SQLite) GetDashboard(id string) (*model.Dashboard, error) {
	rows, err := s.db.Query(`SELECT id, name, revision, schema_version, grid_cols, row_height,
		margin, global_config, created_at, updated_at FROM dashboards WHERE id = ?`, id)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询看板失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询看板失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "dashboard %s not found", id)
	}
	return scanDashboard(rows)
}

// CreateDashboard 落库一个看板。
func (s *SQLite) CreateDashboard(d *model.Dashboard) error {
	if d.ID == "" {
		d.ID = model.NewDashboardID()
	}
	now := model.NowMs()
	if d.CreatedAt == 0 {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.Revision == 0 {
		d.Revision = 1
	}
	if d.GridCols == 0 {
		d.GridCols = model.DefaultGridCols
	}
	if d.RowHeight == 0 {
		d.RowHeight = model.DefaultRowHeight
	}
	if d.Margin == [2]int{0, 0} {
		d.Margin = [2]int{model.DefaultMarginX, model.DefaultMarginY}
	}
	if d.GlobalConfig == nil {
		d.GlobalConfig = map[string]any{}
	}
	marginJSON, err := marshalJSON(d.Margin)
	if err != nil {
		return err
	}
	globalJSON, err := marshalJSON(d.GlobalConfig)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `INSERT INTO dashboards
			(id, name, revision, schema_version, grid_cols, row_height, margin, global_config, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			d.ID, d.Name, d.Revision, d.SchemaVersion, d.GridCols, d.RowHeight, marginJSON, globalJSON, d.CreatedAt, d.UpdatedAt)
		return err
	})
}

// UpdateDashboard 更新看板元数据（含 revision 自增，由调用方校验乐观锁）。
func (s *SQLite) UpdateDashboard(d *model.Dashboard) error {
	d.UpdatedAt = model.NowMs()
	marginJSON, err := marshalJSON(d.Margin)
	if err != nil {
		return err
	}
	globalJSON, err := marshalJSON(d.GlobalConfig)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `UPDATE dashboards SET name = ?, revision = ?, schema_version = ?,
			grid_cols = ?, row_height = ?, margin = ?, global_config = ?, updated_at = ? WHERE id = ?`,
			d.Name, d.Revision, d.SchemaVersion, d.GridCols, d.RowHeight, marginJSON, globalJSON, d.UpdatedAt, d.ID)
		return err
	})
}

// DeleteDashboard 删除看板及其全部卡片。
func (s *SQLite) DeleteDashboard(id string) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `DELETE FROM cards WHERE dashboard_id = ?`, id); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `DELETE FROM dashboards WHERE id = ?`, id)
		return err
	})
}

// ————————————————— Card CRUD —————————————————

// scanCard 把一行映射为 model.Card。
func scanCard(rows *sql.Rows) (*model.Card, error) {
	var (
		c       model.Card
		render  string
		display string
		layout  string
	)
	if err := rows.Scan(&c.ID, &c.DashboardID, &c.ChannelID, &c.RendererType, &c.Title, &c.Unit,
		&render, &display, &layout, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "扫描卡片行失败")
	}
	c.RenderConfig = map[string]any{}
	if err := unmarshalJSON(render, &c.RenderConfig); err != nil {
		return nil, err
	}
	c.DisplayOptions = model.DefaultDisplayOptions()
	if err := unmarshalJSON(display, &c.DisplayOptions); err != nil {
		return nil, err
	}
	c.Layout = model.DefaultLayoutXY()
	if err := unmarshalJSON(layout, &c.Layout); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCards 列出某看板下的全部卡片。
func (s *SQLite) ListCards(dashboardID string) ([]model.Card, error) {
	rows, err := s.db.Query(`SELECT id, dashboard_id, channel_id, renderer_type, title, unit,
		render_config, display_options, layout, created_at, updated_at
		FROM cards WHERE dashboard_id = ? ORDER BY created_at ASC`, dashboardID)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询卡片失败")
	}
	defer rows.Close()
	out := make([]model.Card, 0)
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListAllCards 列出全部卡片。
func (s *SQLite) ListAllCards() ([]model.Card, error) {
	rows, err := s.db.Query(`SELECT id, dashboard_id, channel_id, renderer_type, title, unit,
		render_config, display_options, layout, created_at, updated_at FROM cards ORDER BY created_at ASC`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询卡片失败")
	}
	defer rows.Close()
	out := make([]model.Card, 0)
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetCard 按 ID 读取卡片。
func (s *SQLite) GetCard(id string) (*model.Card, error) {
	rows, err := s.db.Query(`SELECT id, dashboard_id, channel_id, renderer_type, title, unit,
		render_config, display_options, layout, created_at, updated_at FROM cards WHERE id = ?`, id)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询卡片失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询卡片失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "card %s not found", id)
	}
	return scanCard(rows)
}

// cardJSON 预序列化卡片的三处 JSON 列。
func cardJSON(c *model.Card) (render, display, layout string, err error) {
	render, err = marshalJSON(c.RenderConfig)
	if err != nil {
		return "", "", "", err
	}
	display, err = marshalJSON(c.DisplayOptions)
	if err != nil {
		return "", "", "", err
	}
	layout, err = marshalJSON(c.Layout)
	if err != nil {
		return "", "", "", err
	}
	return render, display, layout, nil
}

// CreateCard 落库一张卡片。
func (s *SQLite) CreateCard(c *model.Card) error {
	if c.ID == "" {
		c.ID = model.NewCardID()
	}
	now := model.NowMs()
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	render, display, layout, err := cardJSON(c)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `INSERT INTO cards
			(id, dashboard_id, channel_id, renderer_type, title, unit, render_config, display_options, layout, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.DashboardID, c.ChannelID, c.RendererType, c.Title, c.Unit,
			render, display, layout, c.CreatedAt, c.UpdatedAt)
		return err
	})
}

// UpdateCard 更新一张卡片。
func (s *SQLite) UpdateCard(c *model.Card) error {
	c.UpdatedAt = model.NowMs()
	render, display, layout, err := cardJSON(c)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `UPDATE cards SET dashboard_id = ?, channel_id = ?, renderer_type = ?,
			title = ?, unit = ?, render_config = ?, display_options = ?, layout = ?, updated_at = ? WHERE id = ?`,
			c.DashboardID, c.ChannelID, c.RendererType, c.Title, c.Unit,
			render, display, layout, c.UpdatedAt, c.ID)
		return err
	})
}

// DeleteCard 删除一张卡片。
func (s *SQLite) DeleteCard(id string) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `DELETE FROM cards WHERE id = ?`, id)
		return err
	})
}

// ReplaceCards 全量替换某看板的卡片（看板全量保存时使用，事务内完成）。
func (s *SQLite) ReplaceCards(dashboardID string, cards []model.Card) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `DELETE FROM cards WHERE dashboard_id = ?`, dashboardID); err != nil {
			return err
		}
		stmt, err := t.PrepareContext(ctx, `INSERT INTO cards
			(id, dashboard_id, channel_id, renderer_type, title, unit, render_config, display_options, layout, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := range cards {
			c := cards[i]
			if c.ID == "" {
				c.ID = model.NewCardID()
			}
			if c.CreatedAt == 0 {
				c.CreatedAt = model.NowMs()
			}
			c.UpdatedAt = model.NowMs()
			c.DashboardID = dashboardID
			render, display, layout, err := cardJSON(&c)
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, c.ID, c.DashboardID, c.ChannelID, c.RendererType, c.Title,
				c.Unit, render, display, layout, c.CreatedAt, c.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteCardsByChannel 删除引用某通道的全部卡片。
func (s *SQLite) DeleteCardsByChannel(channelID string) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `DELETE FROM cards WHERE channel_id = ?`, channelID)
		return err
	})
}

// ————————————————— 小工具 —————————————————

// boolToInt 把布尔转成 SQLite 的 0/1。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullString 把空串转成 SQL NULL。
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// ————————————————— Import CRUD（离线导入文件） —————————————————

// scanImport 把一行映射为 model.Import。
func scanImport(rows *sql.Rows) (*model.Import, error) {
	var im model.Import
	if err := rows.Scan(&im.ID, &im.FileName, &im.RelPath, &im.SizeBytes,
		&im.FrameCount, &im.FirstTs, &im.LastTs, &im.CreatedAt); err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "扫描导入文件行失败")
	}
	return &im, nil
}

// ListImports 返回全部导入记录（按创建时间升序）。
func (s *SQLite) ListImports() ([]model.Import, error) {
	rows, err := s.db.Query(`SELECT id, file_name, rel_path, size_bytes, frame_count, first_ts, last_ts, created_at
		FROM imports ORDER BY created_at ASC`)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询导入文件失败")
	}
	defer rows.Close()
	out := make([]model.Import, 0)
	for rows.Next() {
		im, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *im)
	}
	return out, rows.Err()
}

// GetImport 按 ID 读取导入记录，不存在返回 apperr.NotFound。
func (s *SQLite) GetImport(id string) (*model.Import, error) {
	rows, err := s.db.Query(`SELECT id, file_name, rel_path, size_bytes, frame_count, first_ts, last_ts, created_at
		FROM imports WHERE id = ?`, id)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "查询导入文件失败")
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "查询导入文件失败")
		}
		return nil, apperr.Newf(apperr.NotFound, "import %s not found", id)
	}
	im, err := scanImport(rows)
	if err != nil {
		return nil, err
	}
	return im, nil
}

// CreateImport 落库一条导入记录。
func (s *SQLite) CreateImport(im *model.Import) error {
	if im == nil {
		return apperr.New(apperr.InvalidParam, "导入记录为空")
	}
	if im.ID == "" {
		im.ID = model.NewImportID()
	}
	if im.CreatedAt == 0 {
		im.CreatedAt = model.NowMs()
	}
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `INSERT INTO imports
			(id, file_name, rel_path, size_bytes, frame_count, first_ts, last_ts, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			im.ID, im.FileName, im.RelPath, im.SizeBytes,
			im.FrameCount, im.FirstTs, im.LastTs, im.CreatedAt)
		return err
	})
}

// DeleteImport 删除导入记录（磁盘文件由调用方负责清理）。
func (s *SQLite) DeleteImport(id string) error {
	ctx := context.Background()
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `DELETE FROM imports WHERE id = ?`, id)
		return err
	})
}
