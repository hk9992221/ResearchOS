-- ============================================================
-- SPEC-07 Experiments — project.db addition (above the 0200–0299 band)
-- 来源：SPEC-07 §5.2 runs、SPEC-09 §5.3 EntityRun brief。
-- 迁移机制保证本文件只执行一次，故使用普通 ALTER。
-- 版本号 0400 高于既有工作区最高版本（0310），否则不会被执行；
-- 列名与语义登记在 SPEC-07（runs.title）。
-- ============================================================

-- ------------------------------------------------------------
-- 1. runs.title（SPEC-07 §5.2）
-- 人工给运行起的名字，用于识别一次运行；空串表示未命名，展示层回退到
-- 目录名或参数摘要（见 runDisplayName）。不参与 CHECK，保持与
-- experiments.title 一致（NOT NULL DEFAULT ''）。
-- ------------------------------------------------------------
ALTER TABLE runs ADD COLUMN title TEXT NOT NULL DEFAULT '';