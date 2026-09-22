package model

// ————————————————— 离线导入文件 —————————————————

// Import 为一次离线 JSON 文件导入的元数据（落盘 imports 表）。
// 文件本体存于 <dataDir>/imports/<relPath>，此处只保存相对路径。
type Import struct {
	ID         string `json:"id"`
	FileName   string `json:"fileName"` // 原始文件名（仅展示，不参与路径拼接）
	RelPath    string `json:"relPath"`  // 相对 dataDir 的路径，如 imports/im_xxx.json
	SizeBytes  int64  `json:"sizeBytes"`
	FrameCount int    `json:"frameCount"` // 解析出的记录条数
	FirstTs    int64  `json:"firstTs"`    // 首帧时间戳（ms），无时间戳为 0
	LastTs     int64  `json:"lastTs"`     // 末帧时间戳（ms），无时间戳为 0
	CreatedAt  int64  `json:"createdAt"`
}

// NewImport 构造一个带缺省值的导入记录。
func NewImport(fileName, relPath string, sizeBytes int64) *Import {
	return &Import{
		ID:        NewImportID(),
		FileName:  fileName,
		RelPath:   relPath,
		SizeBytes: sizeBytes,
		CreatedAt: NowMs(),
	}
}
