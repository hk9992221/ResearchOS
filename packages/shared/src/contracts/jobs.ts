import type { McpJob } from '../types/mcp'
import type { JobState } from '../types/common'

/** Background-job inspection for the task strip and generation progress. */
export interface JobsCommands {
  'jobs.list': {
    req: { projectId?: string; state?: JobState; limit?: number }
    res: McpJob[]
  }
  'jobs.get': { req: { jobId: string }; res: McpJob }
}