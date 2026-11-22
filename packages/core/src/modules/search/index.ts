/**
 * Unified retrieval layer: extraction and chunking for the eight
 * search sources, the incremental index coordinator and the project/global
 * hybrid search pipeline. Storage primitives live in `../../index-store`.
 */
export * from './types'
export { SearchEngine, INDEX_STATUS, type SearchEngineOptions, type SearchHooks } from './engine'
export { Extractor, type ExtractOptions, type ExtractProblem, type ExtractResult } from './extract'
export { chunkMarkdown, chunkPdfBlocks, estimateTokens, hashText, foldRunes, truncateRunes } from './chunk'
export { PdfJsParser, PdfNoTextLayerError, PdfParserUnavailableError, PdfUnreadableError } from './pdf'
export { normalizeQuery, buildSnippet, detectLang } from './query'
export { runSearch, fuseTarget, mergeResults, sortMerged } from './search'
export { resolveJumpTarget, openJumpTarget, jumpTargetFor } from './target'
export { searchTools, SEARCH_RUNTIME_KEY, type SearchMcpRuntime, type SearchJobPort } from './mcp'
export { aiEmbeddingPort } from './ai-embedding'
