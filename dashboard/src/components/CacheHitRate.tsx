import type { StatsResponse } from '../api'

interface Props {
  stats: StatsResponse | null
}

export default function CacheHitRate({ stats }: Props) {
  const rate = stats?.cache_hit_rate ?? null

  const color =
    rate === null
      ? 'text-gray-500'
      : rate > 40
      ? 'text-green-400'
      : rate > 20
      ? 'text-yellow-400'
      : 'text-red-400'

  const ringColor =
    rate === null
      ? 'border-gray-700'
      : rate > 40
      ? 'border-green-500/40'
      : rate > 20
      ? 'border-yellow-500/40'
      : 'border-red-500/40'

  return (
    <div className="bg-gray-800 rounded-xl p-6 border border-gray-700 flex flex-col items-center">
      <p className="text-xs font-medium text-gray-400 uppercase tracking-widest mb-4">
        Cache Hit Rate
      </p>
      <div
        className={`w-32 h-32 rounded-full border-4 ${ringColor} flex items-center justify-center`}
      >
        <span className={`text-3xl font-bold font-mono ${color}`}>
          {rate !== null ? `${rate.toFixed(1)}%` : '—'}
        </span>
      </div>
      <p className="mt-4 text-xs text-gray-500">
        {rate !== null
          ? rate > 40
            ? 'Excellent — most requests served from cache'
            : rate > 20
            ? 'Moderate — cache warming up'
            : 'Low — consider lowering similarity threshold'
          : 'Waiting for data…'}
      </p>
    </div>
  )
}
