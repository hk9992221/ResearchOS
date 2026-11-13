// Package literature 是 SPEC-04（文献管理）的公开 API 契约（公共契约），
// 逐字转写 docs/specs/SPEC-04-Literature.md §5.0–§5.6 的类型、常量、
// 哨兵错误与服务接口：Zotero 连接与检测、只读元数据访问、PDF 导入、
// 项目关联、文献浏览与 PDF 打开。
//
// 本包含契约声明与 §5.7–§5.10 的扩展类型；实现位于
// internal/literature/library 与 internal/literature/zotero：
// §5.1–§5.6 由 library.Service 直接实现，§5.7–§5.10 通过
// PaperNotes()/Overviews()/Sync()/Maintenance() 门面暴露。
// notes.paper_id / meta_json 由 internal/literature/migrations（0280）
// 迁移带提供，须随 workspace.Options.Migrations 注册。
package literature
