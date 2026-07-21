import { useMemo, useState } from 'react'
import type { Diagnosis } from './types'
import { useDiagnoses } from './useDiagnoses'
import { timeAgo } from './relativeTime'
import './index.css'

function recordKey(d: Diagnosis): string {
  return [d.cluster ?? '', d.namespace, d.pod, d.container].join('/')
}

function searchBlob(d: Diagnosis): string {
  return [d.cluster, d.namespace, d.pod, d.container, d.rootCause, d.summary]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

function severityCounts(diagnoses: Diagnosis[]) {
  const counts = { critical: 0, high: 0, medium: 0, unknown: 0 }
  for (const d of diagnoses) counts[d.severity]++
  return counts
}

function DiagnosisRow({ d, showCluster }: { d: Diagnosis; showCluster: boolean }) {
  const [open, setOpen] = useState(false)
  const hasDetail = Boolean(
    d.rolloutContext || d.logExcerpt || d.tracesURL || (d.recentEvents && d.recentEvents.length > 0),
  )

  return (
    <>
      <tr className="row" onClick={() => setOpen((o) => !o)}>
        {showCluster && (
          <td>
            <span className="cluster">{d.cluster || '-'}</span>
          </td>
        )}
        <td>
          <div className="pod">{d.pod}</div>
          <div className="container">{d.container}</div>
          <div className="ns">{d.namespace}</div>
        </td>
        <td>
          <span className={`badge sev-${d.severity}`}>{d.rootCause}</span>
          <div className="conf">{d.confidence} confidence</div>
        </td>
        <td>{d.restarts}</td>
        <td className="summary">
          {d.summary}
          {!!d.suppressedCount && <span className="conf"> (+{d.suppressedCount} more)</span>}
        </td>
        <td className="rec">{d.recommendation}</td>
        <td>{timeAgo(d.lastObserved ?? d.receivedAt)}</td>
      </tr>
      {open && (
        <tr className="detail">
          <td colSpan={showCluster ? 7 : 6}>
            {d.tracesURL && (
              <a className="traces-link" href={d.tracesURL} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
                View traces in Grafana ↗
              </a>
            )}
            {d.rolloutContext && (
              <>
                <h4>Rollout Context</h4>
                <div className="hint">{d.rolloutContext}</div>
              </>
            )}
            {d.recentEvents && d.recentEvents.length > 0 && (
              <>
                <h4>Recent Events</h4>
                <ul>
                  {d.recentEvents.map((e, i) => (
                    <li key={i}>{e}</li>
                  ))}
                </ul>
              </>
            )}
            {d.logExcerpt ? (
              <>
                <h4>Log Excerpt (previous instance)</h4>
                <pre>{d.logExcerpt}</pre>
              </>
            ) : (
              !hasDetail && <div className="hint">No additional evidence available.</div>
            )}
          </td>
        </tr>
      )}
    </>
  )
}

export default function App() {
  const { diagnoses, loading, error, lastFetchedAt } = useDiagnoses()
  const [query, setQuery] = useState('')

  const showClusterColumn = useMemo(() => diagnoses.some((d) => d.cluster), [diagnoses])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return diagnoses
    return diagnoses.filter((d) => searchBlob(d).includes(q))
  }, [diagnoses, query])

  const counts = useMemo(() => severityCounts(diagnoses), [diagnoses])

  return (
    <div className="page">
      <h1>PodDoctor</h1>
      <div className="sub">
        {loading && diagnoses.length === 0
          ? 'Loading…'
          : `${diagnoses.length} diagnosed failure${diagnoses.length === 1 ? '' : 's'}`}
        {lastFetchedAt && <span className="live"> &middot; live, updated {timeAgo(lastFetchedAt.toISOString())}</span>}
      </div>

      {error && <div className="error">Couldn't reach the API: {error}. Retrying…</div>}

      {diagnoses.length > 0 && (
        <>
          <div className="summary-badges">
            {counts.critical > 0 && <span className="badge sev-critical">{counts.critical} critical</span>}
            {counts.high > 0 && <span className="badge sev-high">{counts.high} high</span>}
            {counts.medium > 0 && <span className="badge sev-medium">{counts.medium} medium</span>}
            {counts.unknown > 0 && <span className="badge sev-unknown">{counts.unknown} unknown</span>}
          </div>
          <div className="search-box">
            <input
              type="text"
              placeholder="Filter by pod, namespace, container, cluster, or root cause..."
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoComplete="off"
            />
          </div>
        </>
      )}

      {!loading && diagnoses.length === 0 && !error && (
        <div className="empty">No crash loops diagnosed. Either everything's healthy, or nothing's crashed yet.</div>
      )}

      {filtered.length > 0 && (
        <table>
          <thead>
            <tr>
              {showClusterColumn && <th>Cluster</th>}
              <th>Pod</th>
              <th>Root Cause</th>
              <th>Restarts</th>
              <th>Summary</th>
              <th>Recommendation</th>
              <th>Last Seen</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((d) => (
              <DiagnosisRow key={recordKey(d)} d={d} showCluster={showClusterColumn} />
            ))}
          </tbody>
        </table>
      )}

      {diagnoses.length > 0 && filtered.length === 0 && (
        <div className="empty">No diagnoses match "{query}".</div>
      )}
    </div>
  )
}
