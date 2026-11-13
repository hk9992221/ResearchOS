// Package literature 是 SPEC-04（文献管理）的公开 API 契约（公共契约），
// 逐字转写 docs/specs/SPEC-04-Literature.md §5.0–§5.6 的类型、常量、
// 哨兵错误与服务接口：Zotero 连接与检测、只读元数据访问、PDF 导入、
// 项目关联、文献浏览与 PDF 打开。
//
// 本包仅含契约声明，不含实现；只有 P1–P6 会被实现。
// §5.7（PaperNoteService）、§5.8（OverviewService）、§5.9（SyncService）
// 不在本契约范围内。
package literature
