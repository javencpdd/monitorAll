// Package version 提供持久化结构版本号的唯一真源。
//
// 背景（H9）：此前 config.CurrentSchemaVersion=1 与 store.CurrentSchemaVersion=2 各自维护一份
// 且已漂移 —— 启动横幅打印 "schema v1"，而数据库实际已在 v2。两者都不会报错，是典型的静默失败。
// 现收敛到本包的单一常量，config 与 store 一律改为别名引用，由 version/version_test.go 锁死。
package version

// SchemaVersion 为数据库结构（meta.schema_version）的当前版本。
//
// 迁移链只增不改：新增迁移时把这个常量 +1，并在 store/schema.go 的 migrations 里追加一条。
//
// ⚠️ 这是【数据库结构】版本，与【配置文件格式】版本 config.ConfigSchemaVersion 是两个不同概念，
// 不要互相赋值或互相校验。
const SchemaVersion = 4

// History 记录各版本的迁移名称，仅供启动日志与排障使用。
var History = map[int]string{
	1: "init",
	2: "add_imports",
	3: "add_mapboard",
	4: "add_app_settings",
}
