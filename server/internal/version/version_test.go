package version_test

import (
	"testing"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/store"
	"github.com/monitorall/monitorall/internal/version"
)

// TestSchemaVersionSingleSource 锁死"数据库结构版本唯一真源"（H9）。
//
// 历史问题：config.CurrentSchemaVersion=1 与 store.CurrentSchemaVersion=2 各写一份且已漂移，
// 启动横幅据此打印错误的 schema 版本，且不报任何错。现在两边都必须引用 version.SchemaVersion，
// 本用例防止有人再引入第二份常量。
func TestSchemaVersionSingleSource(t *testing.T) {
	if store.CurrentSchemaVersion != version.SchemaVersion {
		t.Fatalf("store.CurrentSchemaVersion=%d 与唯一真源 version.SchemaVersion=%d 不一致",
			store.CurrentSchemaVersion, version.SchemaVersion)
	}
	// 配置文件格式版本是另一个概念，允许不同，但必须显式声明而不是悄悄跟随。
	if config.CurrentConfigSchemaVersion != 1 {
		t.Fatalf("配置格式版本变了（%d）？变更需同步更新 config.example.yaml 与迁移说明",
			config.CurrentConfigSchemaVersion)
	}
}

// TestHistoryCoversCurrent 保证每个版本都有可读的迁移名（启动日志与排障依赖）。
func TestHistoryCoversCurrent(t *testing.T) {
	for v := 1; v <= version.SchemaVersion; v++ {
		if name, ok := version.History[v]; !ok || name == "" {
			t.Fatalf("version.History 缺少 v%d 的迁移名", v)
		}
	}
}
