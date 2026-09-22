package api

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/adapter/replay"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 离线 JSON 文件导入（离线回放的数据来源） —————————————————

// 导入相关限制常量（集中定义，禁止散落魔法值）。
const (
	// maxImportSizeBytes 为单个导入文件大小上限（200MB）。
	maxImportSizeBytes = 200 << 20
	// importsRelDir 为导入文件存放目录（相对 dataDir）。
	importsRelDir = "imports"
	// uploadFieldName 为 multipart 表单字段名。
	uploadFieldName = "file"
)

// listImports 返回全部导入记录。
func (d *Deps) listImports(c *gin.Context) {
	list, err := d.Store.ListImports()
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, list)
}

// uploadImport 接收 multipart 上传的 JSON/JSONL 文件：
// 落盘到 <dataDir>/imports/、流式统计帧数与时间跨度、写库并返回导入记录。
func (d *Deps) uploadImport(c *gin.Context) {
	fh, err := c.FormFile(uploadFieldName)
	if err != nil {
		Fail(c, apperr.Wrap(err, apperr.InvalidParam, "缺少上传文件字段 file"))
		return
	}
	if fh.Size <= 0 {
		Fail(c, apperr.New(apperr.InvalidParam, "上传文件为空"))
		return
	}
	if fh.Size > maxImportSizeBytes {
		Fail(c, apperr.Newf(apperr.InvalidParam, "文件超过上限 %dMB", maxImportSizeBytes>>20))
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext != ".json" && ext != ".jsonl" {
		Fail(c, apperr.Newf(apperr.InvalidParam, "仅支持 .json / .jsonl 文件，当前为 %q", ext))
		return
	}

	// 落盘文件名 = 自生成 ID 前缀 + filepath.Base(原名)：既防路径穿越也防覆盖
	rec := model.NewImport(filepath.Base(fh.Filename), "", fh.Size)
	stored := rec.ID + "-" + filepath.Base(fh.Filename)
	relPath := filepath.Join(importsRelDir, stored)
	absPath := filepath.Join(d.Cfg.Server.DataDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		Fail(c, apperr.Wrap(err, apperr.StoreError, "创建导入目录失败"))
		return
	}
	if err := c.SaveUploadedFile(fh, absPath); err != nil {
		Fail(c, apperr.Wrap(err, apperr.StoreError, "保存上传文件失败"))
		return
	}
	rec.RelPath = relPath

	// 流式统计：不把文件读进内存，失败则清理落盘文件后报错
	count, firstTs, lastTs, err := replay.ScanFile(absPath, strings.TrimSpace(c.Query("timePath")))
	if err != nil {
		_ = os.Remove(absPath)
		Fail(c, err)
		return
	}
	if count == 0 {
		_ = os.Remove(absPath)
		Fail(c, apperr.New(apperr.InvalidParam, "文件中没有解析到有效 JSON 记录"))
		return
	}
	rec.FrameCount, rec.FirstTs, rec.LastTs = count, firstTs, lastTs

	if err := d.Store.CreateImport(rec); err != nil {
		_ = os.Remove(absPath)
		Fail(c, err)
		return
	}
	d.Log.Info("导入文件成功", "id", rec.ID, "file", rec.FileName,
		"frames", rec.FrameCount, "firstTs", rec.FirstTs, "lastTs", rec.LastTs)
	Created(c, rec)
}

// deleteImport 删除导入记录与其磁盘文件。
func (d *Deps) deleteImport(c *gin.Context) {
	id := c.Param("id")
	im, err := d.Store.GetImport(id)
	if err != nil {
		Fail(c, err)
		return
	}
	if err := d.Store.DeleteImport(id); err != nil {
		Fail(c, err)
		return
	}
	// 库记录已删，磁盘文件清理失败只记日志，不影响响应
	absPath := filepath.Join(d.Cfg.Server.DataDir, im.RelPath)
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		d.Log.Warn("删除导入文件失败", "path", absPath, "err", err)
	}
	OK(c, map[string]any{"deleted": true})
}
