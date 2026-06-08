import type { StatsResponse } from '../api'

interface Props {
  stats: StatsResponse | null
}

function formatCost(usd: number): string {
  if (usd === 0) return '$0.0000'
  if (usd < 0.0001) return `$${usd.toExponential(2)}`
  return `$${usd.toFixed(4)}`
}

export default function CostTracker({ stats }: Props) {
  const cost = stats?.cost_today_usd ?? null
  const totalRequests = stats?.total_requests ?? null

  return (
    <div className="bg-gray-800 rounded-xl p-6 border border-gray-700">
      <p className="text-xs font-medium text-gray-400 uppercase tracking-widest mb-4">
        Cost Today
      </p>

      <p className="text-4xl font-bold font-mono text-white">
        {cost !== null ? formatCost(cost) : '—'}
      </p>

      <div className="mt-4 grid grid-cols-2 gap-3">
        <div className="bg-gray-700/50 rounded-lg p-3">
          <p className="text-xs text-gray-400 mb-1">Total Requests</p>
          <p className="text-lg font-mono font-semibold text-white">
            {totalRequests !== null ? totalRequests.toLocaleString() : '—'}
          </p>
        </div>
        <div className="bg-gray-700/50 rounded-lg p-3">
          <p className="text-xs text-gray-400 mb-1">Avg Latency</p>
          <p className="text-lg font-mono font-semibold text-white">
            {stats?.avg_latency_ms !== undefined
              ? `${Math.round(stats.avg_latency_ms)}ms`
              : '—'}
          </p>
        </div>
      </div>
    </div>
  )
}
