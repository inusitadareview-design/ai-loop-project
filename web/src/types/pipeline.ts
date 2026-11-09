// Types for pipeline CRUD operations (Sprint 15).

export interface Schedule {
  enabled: boolean
  cron: string
}

export interface Model {
  preferred: string
  fallbacks?: string[]
  max_tokens: number
  temperature: number
}

export interface Stage {
  id: string
  action: 'generate' | 'self_review' | 'http_post'
  depends_on?: string[]
  prompt_file?: string
  input_keys?: string[]
  output_key?: string
  criteria?: string[]
  endpoint_env?: string
}

export interface Limits {
  require_review: boolean
  max_cost_per_run_usd: number
  max_retries_per_stage: number
  dead_loop_failures: number
}

export interface Cache {
  enabled: boolean
  ttl_minutes: number
}

export interface Pipeline {
  name: string
  description: string
  schedule: Schedule
  model: Model
  stages: Stage[]
  limits: Limits
  cache: Cache
  locked?: boolean
  paused?: boolean
}

export interface TemplateSummary {
  name: string
  path: string
  size_bytes: number
}

// Sprint 15 15b-frontend: Schedule management types

export interface ScheduledPipeline {
  name: string
  cron: string
  enabled: boolean
  next_execution: string // ISO 8601 timestamp from backend
  locked: boolean
  paused: boolean
}

export interface ScheduleStatus {
  timezone: string
  pipelines: ScheduledPipeline[]
}