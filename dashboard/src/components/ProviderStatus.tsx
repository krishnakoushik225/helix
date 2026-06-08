import { useEffect, useState } from 'react'
import { fetchProviders, type ProviderHealth, type StatsResponse } from '../api'

interface Props {
  token: string
  stats: StatsResponse | null
}

const STATE_CONFIG = {
  closed: {
    dot: 'bg-green-400',
    badge: 'bg-green-900/40 text-green-300 border border-green-700/40',
    label: 'Closed',
  },
  half_open: {
    dot: 'bg-yellow-400 animate-pulse',
    badge: 'bg-yellow-900/40 text-yellow-300 border border-yellow-700/40',
    label: 'Half-Open',
  },
  open: {
    dot: 'bg-red-400',
    badge: 'bg-red-900/40 text-red-300 border border-red-700/40',
    label: 'Open',
  },
  unknown: {
    dot: 'bg-gray-500',
    badge: 'bg-gray-700/40 text-gray-400 border border-gray-600/40',
    label: 'Unknown',
  },
}

export default function ProviderStatus({ token, stats }: Props) {
  const [providers, setProviders] = useState<ProviderHealth[]>([])
  const [apiAvailable, setApiAvailable] = useState(false)

  useEffect(() => {
    let cancelled = false

    const poll = async () => {
      try {
        const data = await fetchProviders(token)
        if (!cancelled) {
          setProviders(data)
          setApiAvailable(true)
        }
      } catch {
        if (!cancelled) setApiAvailable(false)
      }
    }

    poll()
    const id = setInterval(poll, 15_000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [token])

  // Fallback: infer provider list from stats breakdown when /v1/providers is absent
  const displayProviders: ProviderHealth[] = apiAvailable
    ? providers
    : Object.keys(stats?.provider_breakdown ?? {}).map((name) => ({
        name,
        state: 'unknown' as const,
      }))

  return (
    <div className="bg-gray-800 rounded-xl p-6 border border-gray-700">
      <div className="flex items-center justify-between mb-4">
        <p className="text-xs font-medium text-gray-400 uppercase tracking-widest">
          Provider Status
        </p>
        {!apiAvailable && (
          <span className="text-xs text-gray-600 italic">circuit data unavailable</span>
        )}
      </div>

      {displayProviders.length === 0 ? (
        <p className="text-gray-500 text-sm text-center py-4">No providers registered</p>
      ) : (
        <div className="space-y-3">
          {displayProviders.map((p) => {
            const cfg = STATE_CONFIG[p.state] ?? STATE_CONFIG.unknown
            const bd = stats?.provider_breakdown[p.name]
            return (
              <div
                key={p.name}
                className="flex items-center justify-between bg-gray-700/40 rounded-lg px-4 py-3"
              >
                <div className="flex items-center gap-3">
                  <span className={`w-2.5 h-2.5 rounded-full ${cfg.dot}`} />
                  <span className="text-sm font-medium text-white capitalize">{p.name}</span>
                </div>
                <div className="flex items-center gap-3">
                  {bd && (
                    <span className="text-xs text-gray-400 font-mono">
                      {Math.round(bd.avg_latency_ms)}ms
                    </span>
                  )}
                  <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${cfg.badge}`}>
                    {cfg.label}
                  </span>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
