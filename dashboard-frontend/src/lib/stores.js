import { writable, derived } from 'svelte/store'

// ─── WebSocket connection ───
export const wsConnected = writable(false)
export const wsMessages = writable([])

let ws = null
let reconnectTimer = null

export function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/ws`)

  ws.onopen = () => {
    wsConnected.set(true)
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
  }

  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      handleMessage(msg)
    } catch (e) {
      console.error('WS parse error:', e)
    }
  }

  ws.onclose = () => {
    wsConnected.set(false)
    if (!reconnectTimer) {
      reconnectTimer = setTimeout(() => { reconnectTimer = null; connectWS() }, 3000)
    }
  }

  ws.onerror = () => { ws.close() }
}

export function sendWS(msg) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(msg))
  }
}

// ─── Data stores ───
export const activeRuns = writable([])
export const taskStats = writable({})
export const tasks = writable([])
export const projects = writable([])
export const agents = writable([])
export const runLogs = writable({}) // { runId: [lines] }
export const runEvents = writable({}) // { runId: [events] }

function handleMessage(msg) {
  switch (msg.type) {
    case 'snapshot':
      activeRuns.set(msg.data.active_runs || [])
      // task_stats comes as [{status, count}, ...] — convert to {status: count}
      if (Array.isArray(msg.data.task_stats)) {
        const statsObj = {}
        for (const s of msg.data.task_stats) statsObj[s.status] = s.count
        taskStats.set(statsObj)
      } else {
        taskStats.set(msg.data.task_stats || {})
      }
      projects.set(msg.data.projects || [])
      // Load initial log lines for each active run
      if (msg.data.initial_logs) {
        runLogs.update(logs => {
          const updated = { ...logs }
          for (const [runId, lines] of Object.entries(msg.data.initial_logs)) {
            updated[runId] = lines.map(l => ({ line: l, timestamp: '' }))
          }
          return updated
        })
      }
      break
    case 'run_update':
      activeRuns.update(runs => {
        const data = msg.data
        const idx = runs.findIndex(r => r.id === data.id)
        if (idx >= 0) {
          runs[idx] = { ...runs[idx], ...data }
        } else {
          runs.push(data)
        }
        return runs
      })
      break
    case 'log_line':
      runLogs.update(logs => {
        const id = msg.data.run_id
        if (!logs[id]) logs[id] = []
        logs[id].push(msg.data)
        if (logs[id].length > 50) logs[id] = logs[id].slice(-50)
        return { ...logs }
      })
      break
    case 'event':
      runEvents.update(events => {
        const id = msg.data.run_id
        if (!events[id]) events[id] = []
        events[id].push(msg.data)
        return { ...events }
      })
      // Update tool_use_count
      if (msg.data.type === 'tool_call') {
        activeRuns.update(runs => {
          const idx = runs.findIndex(r => r.id === msg.data.run_id)
          if (idx >= 0) {
            runs[idx] = { ...runs[idx], tool_use_count: (runs[idx].tool_use_count || 0) + 1 }
          }
          return runs
        })
      }
      break
    case 'task_update':
      tasks.update(all => {
        const idx = all.findIndex(t => t.id === msg.data.id)
        if (idx >= 0) {
          all[idx] = { ...all[idx], ...msg.data }
        }
        return all
      })
      break
    case 'run_done':
      activeRuns.update(runs => runs.filter(r => r.id !== msg.data.id))
      break
    case 'task_stats':
      taskStats.set(msg.data)
      break
  }
}

// ─── API client ───
const apiBase = '/api/v1'

export async function apiGet(path) {
  const r = await fetch(`${apiBase}${path}`)
  if (!r.ok) throw new Error(`API ${path}: ${r.status}`)
  return r.json()
}

export async function apiPost(path, body) {
  const r = await fetch(`${apiBase}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!r.ok) throw new Error(`API ${path}: ${r.status}`)
  return r.json()
}

export async function apiPatch(path, body) {
  const r = await fetch(`${apiBase}${path}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!r.ok) throw new Error(`API ${path}: ${r.status}`)
  return r.json()
}

// ─── Avatar logic ───
export function getToolTier(pct) {
  if (pct < 30) return 'low'
  if (pct < 70) return 'mid'
  return 'high'
}

export function getRedoTier(redo) {
  if (redo <= 3) return 'low'
  if (redo <= 7) return 'mid'
  return 'high'
}

export function getAvatarKey(toolPct, redo) {
  const t = getToolTier(toolPct)
  const r = getRedoTier(redo)
  return `${t}_${r}` // e.g. "low_mid"
}

// ─── Format helpers ───
export function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
}

export function formatDuration(start, end) {
  if (!start) return ''
  const s = new Date(start)
  const e = end ? new Date(end) : new Date()
  const mins = Math.floor((e - s) / 60000)
  if (mins < 60) return `${mins}m`
  return `${Math.floor(mins / 60)}h ${mins % 60}m`
}