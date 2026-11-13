package literature

import "errors"

// 错误使用哨兵错误，调用方用 errors.Is 判定；对外错误信息中文、可操作。
// 来源：SPEC-04 §5.0。
var (
	ErrZoteroNotConfigured  = errors.New("zotero: 数据目录未配置")
	ErrZoteroDirInvalid     = errors.New("zotero: 数据目录无效")
	ErrZoteroUnavailable    = errors.New("zotero: 本地服务与数据库均不可用")
	ErrLocalAPIDisabled     = errors.New("zotero: 本地 API 未启用")
	ErrZoteroBusy           = errors.New("zotero: 数据库被占用")
	ErrItemNotFound         = errors.New("zotero: 条目不存在")
	ErrNoPDFAttachment      = errors.New("zotero: 条目没有 PDF 附件")
	ErrAttachmentChoice     = errors.New("zotero: 需要选择 PDF 附件")
	ErrAttachmentUnresolved = errors.New("zotero: 附件路径无法解析")
	ErrPathOutsideStorage   = errors.New("zotero: 附件路径越出数据目录")
	ErrPaperFileMissing     = errors.New("literature: 文献副本丢失")
	ErrPaperNotFound        = errors.New("literature: 文献不存在")
	ErrProjectArchived      = errors.New("literature: 项目已归档，只读")
	ErrProjectLinkExists    = errors.New("literature: 项目已关联该文献")
	ErrProjectLinkMissing   = errors.New("literature: 项目未关联该文献")
	ErrPaperNoteExists      = errors.New("literature: 该项目已存在该论文的 Paper Note")
	ErrNoteDeleted          = errors.New("literature: Paper Note 已删除")
	ErrSyncReportExpired    = errors.New("literature: 同步差异报告已过期，请重新比对")
	ErrSyncSourceChanged    = errors.New("literature: 源文件在比对后发生变化")
	ErrSyncConflict         = errors.New("literature: 副本哈希与预期不一致")
	ErrConfirmationRequired = errors.New("literature: 该操作需要用户在应用内确认")
	ErrBusy                 = errors.New("literature: 另一导入或同步任务正在运行")
	ErrInvalidPDF           = errors.New("literature: 附件不是 PDF")
)
