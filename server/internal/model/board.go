package model

// ————————————————— 看板与卡片 —————————————————

// Layout 为卡片在看板网格中的位置与尺寸。
type Layout struct {
	X    int `json:"x"`
	Y    int `json:"y"`
	W    int `json:"w"`
	H    int `json:"h"`
	MinW int `json:"minW"` // 默认 2
	MinH int `json:"minH"` // 默认 2
}

// DisplayOptions 为卡片页脚展示配置。
type DisplayOptions struct {
	ShowFooter   bool     `json:"showFooter"`   // 默认 true
	Decimals     int      `json:"decimals"`     // 默认 2
	FooterFields []string `json:"footerFields"` // 默认 ["lastUpdate","rate","latency","source"]
}

// Card 为看板卡片实体。
type Card struct {
	ID             string         `json:"id"`
	DashboardID    string         `json:"dashboardId"`
	ChannelID      string         `json:"channelId"`
	RendererType   string         `json:"rendererType"`
	Title          string         `json:"title"`
	Unit           string         `json:"unit,omitempty"`
	RenderConfig   map[string]any `json:"renderConfig"` // 由 RendererManifest.configSchema 驱动
	DisplayOptions DisplayOptions `json:"displayOptions"`
	Layout         Layout         `json:"layout"`
	CreatedAt      int64          `json:"createdAt"`
	UpdatedAt      int64          `json:"updatedAt"`
}

// Dashboard 为看板实体，revision 为乐观锁。
type Dashboard struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Revision      int64          `json:"revision"`      // 乐观锁，每次保存 +1
	SchemaVersion int            `json:"schemaVersion"` // 1
	GridCols      int            `json:"gridCols"`      // 默认 12
	RowHeight     int            `json:"rowHeight"`     // 默认 30（px，配合 margin 12）
	Margin        [2]int         `json:"margin"`        // [12,12]
	GlobalConfig  map[string]any `json:"globalConfig"`  // theme / defaultRefreshRate / amapStyle ...
	CreatedAt     int64          `json:"createdAt"`
	UpdatedAt     int64          `json:"updatedAt"`
}

// DashboardWithCards 为看板及其卡片（GET /dashboards/:id 与全量保存的返回体）。
type DashboardWithCards struct {
	Dashboard
	Cards []Card `json:"cards"`
}

// ————————————————— 网格默认值常量（与前端 constants.ts 对齐） —————————————————

const (
	// DefaultGridCols 为默认网格列数。
	DefaultGridCols = 12
	// DefaultRowHeight 为默认行高（px）。
	DefaultRowHeight = 30
	// DefaultMarginX / DefaultMarginY 为默认网格间距。
	DefaultMarginX = 12
	DefaultMarginY = 12
	// DefaultMinCardW / DefaultMinCardH 为卡片最小尺寸。
	DefaultMinCardW = 2
	DefaultMinCardH = 2
	// DefaultDecimals 为默认小数位。
	DefaultDecimals = 2
	// DefaultCardW / DefaultCardH 为新建卡片的默认尺寸。
	DefaultCardW = 3
	DefaultCardH = 4
)

// DefaultFooterFields 为默认页脚字段。
var DefaultFooterFields = []string{"lastUpdate", "rate", "latency", "source"}

// DefaultDisplayOptions 返回卡片默认展示配置。
func DefaultDisplayOptions() DisplayOptions {
	fields := make([]string, len(DefaultFooterFields))
	copy(fields, DefaultFooterFields)
	return DisplayOptions{
		ShowFooter:   true,
		Decimals:     DefaultDecimals,
		FooterFields: fields,
	}
}

// DefaultLayoutXY 返回默认卡片布局（放在网格左上角）。
func DefaultLayoutXY() Layout {
	return Layout{
		X:    0,
		Y:    0,
		W:    DefaultCardW,
		H:    DefaultCardH,
		MinW: DefaultMinCardW,
		MinH: DefaultMinCardH,
	}
}

// NewDashboard 构造一个带缺省值的看板。
func NewDashboard(name string) *Dashboard {
	now := NowMs()
	return &Dashboard{
		ID:            NewDashboardID(),
		Name:          name,
		Revision:      1,
		SchemaVersion: CurrentSchemaVersionForEntity,
		GridCols:      DefaultGridCols,
		RowHeight:     DefaultRowHeight,
		Margin:        [2]int{DefaultMarginX, DefaultMarginY},
		GlobalConfig:  map[string]any{},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// NewCard 构造一个带缺省值的卡片。
func NewCard(dashboardID, channelID, rendererType, title string) *Card {
	now := NowMs()
	return &Card{
		ID:             NewCardID(),
		DashboardID:    dashboardID,
		ChannelID:      channelID,
		RendererType:   rendererType,
		Title:          title,
		RenderConfig:   map[string]any{},
		DisplayOptions: DefaultDisplayOptions(),
		Layout:         DefaultLayoutXY(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}
