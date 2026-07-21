// Mirrors the JSON shape both internal/dashboard and internal/hub emit
// from /api/diagnoses — same frontend renders either.
export interface Diagnosis {
  cluster?: string
  namespace: string
  pod: string
  container: string
  rootCause: string
  severity: 'critical' | 'high' | 'medium' | 'unknown'
  confidence: string
  restarts: number
  summary: string
  recommendation: string
  rolloutContext?: string
  logExcerpt?: string
  recentEvents?: string[]
  suppressedCount?: number
  lastObserved?: string
  receivedAt?: string
}
