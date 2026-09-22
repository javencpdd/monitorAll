package replay

import (
	"os"

	"github.com/monitorall/monitorall/internal/apperr"
)

// ScanFile 流式扫描导入文件，统计记录条数与首末帧时间戳（ms）。
// 与回放共用同一解析器，保证上传时统计出的 frame_count/first_ts/last_ts
// 与实际回放节奏完全一致。全程不把文件读进内存。
func ScanFile(path, timePath string) (frameCount int, firstTs, lastTs int64, err error) {
	f, openErr := os.Open(path)
	if openErr != nil {
		return 0, 0, 0, apperr.Wrap(openErr, apperr.AdapterError, "打开导入文件失败")
	}
	defer f.Close()

	iterErr := iterateRecords(f, func(raw any) error {
		frameCount++
		ts := int64(0)
		if timePath != "" {
			if v, ok := pick(raw, timePath); ok {
				ts = extractTs(v)
			}
		}
		if ts == 0 {
			ts = extractTs(raw)
		}
		if ts > 0 {
			if firstTs == 0 || ts < firstTs {
				firstTs = ts
			}
			if ts > lastTs {
				lastTs = ts
			}
		}
		return nil
	})
	if iterErr != nil {
		return frameCount, firstTs, lastTs, apperr.Wrap(iterErr, apperr.AdapterError, "解析导入文件失败")
	}
	return frameCount, firstTs, lastTs, nil
}
