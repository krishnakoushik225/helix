import { useEffect, useRef, useState } from 'react'
import type { StatsResponse } from '../api'

interface FeedRow {
  id: string
  polledAt: Date
  provider: string
  avgLatencyMs: number
  requests: number
  costUSD: number
  /** Inferred from cache_hit_rate snapshot — true if rate > 30 % at poll time */
  cacheHitLikely: boolean
}

interface Props {
  stats: StatsResponse | null
}

const MAX_ROWS = 20

function formatTime(d: Date): string {
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function formatCost(usd: number): string {
  if (usd === 0) return '$0.00'
  if (usd < 0.0001) return `$${usd.toExponential(1)}`
  return `$${usd.toFixed(4)}`
}

export default function RequestFeed({ stats }: Props) {
  const [rows, setRows] = useState<FeedRow[]>([])
  const prevStatsRef = useRef<StatsResponse | null>(null)

  useEffect(() => {
    if (!stats) return
    const prev = prevStatsRef.current
    prevStatsRef.current = stats

    // Skip if this is the same data we already processed
    if (prev && prev.total_requests === stats.total_requests) return

    const now = new Date()
    const cacheHitLikely = stats.cache_hit_rate > 30

    const newRows: FeedRow[] = Object.entries(stats.provider_breakdown).map(
      ([provider, bd]) => ({
        id: `${now.getTime()}-${provider}`,
        polledAt: now,
        provider,
        avgLatencyMs: bd.avg_latency_ms,
        requests: bd.requests,
        costUSD: bd.cost_usd,
        cacheHitLikely,
      }),
    )

    setRows((prev) => [...newRows, ...prev].slice(0, MAX_ROWS))
  }, [stats])

  return (
    <div className="bg-gray-800 rounded-xl border border-gray-700 flex flex-col">
      <div className="flex items-center justify-between px-6 py-4 border-b border-gray-700">
        <p className="text-xs font-medium text-gray-400 uppercase tracking-widest">
          Request Feed
        </p>
        <span className="text-xs text-gray-500">(aggregate per provider · last {MAX_ROWS} snapshots)</span>
      </div>

      <div className="overflow-auto max-h-72">
        {rows.length === 0 ? (
          <p className="text-gray-500 text-sm text-center py-8">
            Waiting for first poll…
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-gray-700 text-xs text-gray-500 uppercase">
                <th className="text-left px-6 py-2 font-medium">Time</th>
                <th className="text-left px-4 py-2 font-medium">Provider</th>
                <th className="text-right px-4 py-2 font-medium">Latency</th>
                <th className="text-right px-4 py-2 font-medium">Requests</th>
                <th className="text-center px-4 py-2 font-medium">Cache</th>
                <th className="text-right px-6 py-2 font-medium">Cost</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.id}
                  className="border-b border-gray-700/50 hover:bg-gray-700/30 transition-colors"
                >
                  <td className="px-6 py-2.5 font-mono text-gray-400 text-xs whitespace-nowrap">
                    {formatTime(row.polledAt)}
                  </td>
                  <td className="px-4 py-2.5">
                    <span className="capitalize text-white font-medium">{row.provider}</span>
                  </td>
                  <td className="px-4 py-2.5 text-right font-mono text-gray-300">
                    {Math.round(row.avgLatencyMs)}ms
                  </td>
                  <td className="px-4 py-2.5 text-right font-mono text-gray-300">
                    {row.requests.toLocaleString()}
                  </td>
                  <td className="px-4 py-2.5 text-center">
                    {row.cacheHitLikely ? (
                      <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-green-900/50 text-green-300 border border-green-700/40">
                        HIT
                      </span>
                    ) : (
                      <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-gray-700/50 text-gray-400 border border-gray-600/40">
                        MISS
                      </span>
                    )}
                  </td>
                  <td className="px-6 py-2.5 text-right font-mono text-gray-300">
                    {formatCost(row.costUSD)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
