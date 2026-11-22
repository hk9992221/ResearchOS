/**
 * Fixtures shared by the experiments renderer tests. They are cast-free where
 * the contract allows it; wire payloads the test overrides are typed at the
 * call site.
 */

import type {
  ComparisonTable,
  ExperimentDetail,
  ExperimentSummary,
  ExperimentView,
  GitInfo,
  RunSummary,
  ScanFileEntry,
  ScanPlan,
  SnapshotDetail,
  SnapshotSummary
} from '@researchos/shared'

const T0 = '2026-02-01T10:00:00.000Z'

export function experimentView(overrides: Partial<ExperimentView> = {}): ExperimentView {
  return {
    id: 'e1',
    title: 'Ablation study',
    purpose: 'Measure the effect of learning rate',
    dirRef: 'F:/runs/ablation',
    scanRules: {
      include: ['**/*'],
      exclude: ['.git/'],
      maxFileBytes: 512 * 1024,
      maxTotalBytes: 4 * 1024 * 1024,
      maxFiles: 20_000,
      hashMaxBytes: 64 * 1024 * 1024,
      includeImagesInAi: false
    },
    createdAt: T0,
    updatedAt: T0,
    deletedAt: null,
    ...overrides
  }
}

export function runSummary(overrides: Partial<RunSummary> = {}): RunSummary {
  return {
    id: 'r1',
    title: 'baseline',
    displayName: 'baseline',
    dirRef: null,
    params: { lr: { value: '0.1' }, batch: { value: '32' } },
    paramCount: 2,
    status: 'finished',
    lastAnalyzedAt: null,
    deletedAt: null,
    ...overrides
  }
}

export function snapshotSummary(overrides: Partial<SnapshotSummary> = {}): SnapshotSummary {
  return {
    id: 's1',
    targetType: 'experiment',
    targetId: 'e1',
    kind: 'outline',
    createdAt: T0,
    providerName: 'OpenAI',
    modelId: 'gpt-4o',
    gitCommit: 'abcdef1234567',
    metricCount: 2,
    warnings: 0,
    aiStatus: 'draft',
    humanEdited: false,
    confirmedAt: null,
    ...overrides
  }
}

export function experimentSummary(overrides: Partial<ExperimentSummary> = {}): ExperimentSummary {
  return {
    id: 'e1',
    title: 'Ablation study',
    purpose: 'Measure the effect of learning rate',
    dirRef: 'F:/runs/ablation',
    dirStatus: 'ok',
    dirReason: '',
    runCount: 2,
    lastRunStatus: 'finished',
    lastAnalyzedAt: T0,
    linkedIdeas: 1,
    updatedAt: T0,
    deletedAt: null,
    ...overrides
  }
}

export function gitInfo(overrides: Partial<GitInfo> = {}): GitInfo {
  return {
    detected: true,
    available: true,
    repoRoot: 'F:/runs/ablation',
    branch: 'main',
    commit: 'abcdef1234567890',
    commitShort: 'abcdef1',
    commitTime: T0,
    dirty: true,
    changedFiles: 2,
    untrackedFiles: 1,
    capturedAt: T0,
    source: 'runtime_metadata',
    truncated: false,
    error: null,
    ...overrides
  }
}

export function scanEntry(overrides: Partial<ScanFileEntry> = {}): ScanFileEntry {
  return {
    path: 'train.py',
    kind: 'code',
    size: 2048,
    mtime: T0,
    sha256: 'a'.repeat(64),
    hashSkipped: null,
    contentEligible: true,
    exclusionReason: null,
    sampled: false,
    ...overrides
  }
}

export function scanPlan(overrides: Partial<ScanPlan> = {}): ScanPlan {
  return {
    target: { kind: 'experiment', id: 'e1' },
    root: 'F:/runs/ablation',
    entries: [
      scanEntry(),
      scanEntry({
        path: 'weights/model.bin',
        kind: 'binary',
        size: 4096,
        sha256: null,
        hashSkipped: 'binary',
        contentEligible: false,
        exclusionReason: 'model_weight'
      })
    ],
    pruned: [{ path: 'cache/', reason: 'cache' }],
    totalFiles: 2,
    totalBytes: 6144,
    includedFiles: 1,
    includedBytes: 2048,
    contextBytes: 2048,
    warnings: [{ code: 'SYMLINK', message: 'Skipped an escaping symlink' }],
    truncated: false,
    ...overrides
  }
}

export function experimentDetail(overrides: Partial<ExperimentDetail> = {}): ExperimentDetail {
  const experiment = experimentView()
  return {
    experiment,
    dirRefs: [
      {
        refId: 'd1',
        ownerType: 'experiment',
        ownerId: experiment.id,
        path: experiment.dirRef as string,
        status: 'ok',
        exists: true,
        reason: '',
        lastCheckedAt: T0
      }
    ],
    runs: [runSummary()],
    recentSnapshots: [snapshotSummary()],
    linkedIdeas: [],
    ...overrides
  }
}

export function snapshotDetail(overrides: Partial<SnapshotDetail> = {}): SnapshotDetail {
  return {
    id: 's1',
    targetType: 'experiment',
    targetId: 'e1',
    createdAt: T0,
    fileManifest: [scanEntry()],
    gitInfo: gitInfo(),
    metrics: {
      v: 1,
      metrics: [
        {
          key: 'accuracy',
          label: 'accuracy',
          value: 0.8,
          raw: '0.8000',
          unit: null,
          source: { path: 'metrics.json', pointer: '/accuracy', line: 1, sha256: 'b'.repeat(64) },
          extractor: 'json'
        }
      ],
      unparsed: []
    },
    aiAnalysis: {
      v: 1,
      kind: 'outline',
      aiOutputId: 'o1',
      promptVersion: 'exp-outline-v1',
      contentMd: '# Outline\n\nResult text',
      contentSha256: 'c'.repeat(64),
      sections: [
        {
          key: 'goal',
          title: 'Goal',
          textMd: 'Measure the learning rate effect.',
          sources: [{ path: 'train.py', sha256: 'a'.repeat(64) }],
          unconfirmed: false,
          unconfirmedReason: null
        }
      ],
      unverifiedClaims: [],
      config: { scanRules: {}, runParams: {}, extracted: [] },
      warnings: []
    },
    modelInfo: {
      v: 1,
      providerId: 'p1',
      providerName: 'OpenAI',
      protocol: 'openai-chat',
      modelId: 'gpt-4o',
      promptVersion: 'exp-outline-v1',
      parameters: {},
      sourceRefs: [
        {
          entityType: 'experiment',
          entityId: 'e1',
          locator: 'ros://experiment/11111111-1111-4111-8111-111111111111',
          title: 'Ablation study'
        }
      ],
      generatedAt: T0
    },
    ...overrides
  }
}

export function comparisonTable(overrides: Partial<ComparisonTable> = {}): ComparisonTable {
  return {
    format: 'latex_booktabs',
    table: 'TABLE',
    baselineId: 'r1',
    runs: [
      { runId: 'r1', label: 'baseline', gitShortSha: 'abcdef1', dirty: false, status: 'finished' },
      { runId: 'r2', label: 'tuned', gitShortSha: '1234567', dirty: true, status: 'finished' }
    ],
    paramRows: [{ key: 'lr', values: { r1: '0.1', r2: '0.05' }, isDiff: true }],
    metricRows: [
      {
        key: 'accuracy',
        values: { r1: 0.8, r2: 0.9 },
        delta: { r2: 0.1 },
        pct: { r2: 12.5 },
        bestRunId: 'r2',
        secondBestRunId: 'r1',
        direction: 'higher'
      },
      {
        key: 'loss',
        values: { r1: 0.5, r2: 0.4 },
        delta: { r2: -0.1 },
        pct: { r2: -20 },
        bestRunId: 'r2',
        secondBestRunId: 'r1',
        direction: 'lower'
      }
    ],
    ...overrides
  }
}
