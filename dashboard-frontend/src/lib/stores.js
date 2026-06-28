import { writable, derived, get } from 'svelte/store'

// ─── WebSocket connection ───
export const wsConnected = writable(false)
export const wsMessages = writable([])

let ws = null
let reconnectTimer = null
let pendingSends = []  // messages queued before WS is open

export function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/ws`)

  ws.onopen = () => {
    wsConnected.set(true)
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    // Flush pending messages that were queued before WS was open
    for (const msg of pendingSends) {
      ws.send(JSON.stringify(msg))
    }
    pendingSends = []
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
  } else {
    // Queue message until WS is open
    pendingSends.push(msg)
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

// ─── Chat-centric stores (new concept) ───
export const tree = writable(null)           // workspace → projects → tasks tree
export const livePreviews = writable([])     // streaming chat previews
export const currentChat = writable({        // currently selected chat
  type: 'workspace',  // 'workspace' | 'project' | 'task' | 'run'
  id: 'workspace',
  title: 'Control Room',
})
export const conversation = writable([])     // messages for current chat (comments + events)
export const controllerMessages = writable([]) // DEPRECATED — use agentStreams instead
export const agentStreams = writable({})     // map: nodeKey → messages[] (live agent output per node)
export const agentRunning = writable({})    // map: nodeKey → boolean (is agent running for this node)
export const sessionMessages = writable({}) // map: nodeKey → structured messages from state.db

function handleMessage(msg) {
  switch (msg.type) {
    case 'session_message': {
      // Structured message from SessionReader (state.db)
      // msg: { type, id, role, content, tool_name, tool_calls, timestamp }
      const cc = get(currentChat)
      let nodeKey
      if (cc.type === 'workspace') nodeKey = 'workspace'
      else if (cc.type === 'project') nodeKey = 'project:' + cc.id
      else break

      sessionMessages.update(streams => {
        if (!streams[nodeKey]) streams[nodeKey] = []
        // Avoid duplicates by message ID
        if (streams[nodeKey].some(m => m.id === msg.id)) return streams
        streams[nodeKey] = [...streams[nodeKey], {
          id: msg.id,
          role: msg.role,
          content: msg.content,
          toolName: msg.tool_name,
          toolCalls: msg.tool_calls,
          timestamp: msg.timestamp,
        }]
        return { ...streams }
      })
      break
    }
    case 'snapshot':
      activeRuns.set(msg.data.active_runs || [])
      if (Array.isArray(msg.data.task_stats)) {
        const statsObj = {}
        for (const s of msg.data.task_stats) statsObj[s.status] = s.count
        taskStats.set(statsObj)
      } else {
        taskStats.set(msg.data.task_stats || {})
      }
      projects.set(msg.data.projects || [])
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
      // Also update live previews
      refreshLivePreviews()
      break
    case 'log_line':
      runLogs.update(logs => {
        const id = msg.data.run_id
        if (!logs[id]) logs[id] = []
        logs[id].push(msg.data)
        if (logs[id].length > 50) logs[id] = logs[id].slice(-50)
        return { ...logs }
      })
      // Also append to conversation if this is a task chat
      // and the run belongs to the current task (match by run_id from live previews)
      currentChat.update(cc => {
        if (cc.type === 'task') {
          // Match: check if this run_id belongs to the current task
          // We use livePreviews which has task_id → run_id mapping
          const previews = get(livePreviews)
          const preview = previews.find(p => p.run_id === msg.data.run_id)
          if (preview && preview.id === cc.id) {
            conversation.update(msgs => [...msgs, {
              role: 'log',
              body: msg.data.line,
              timestamp: msg.data.timestamp || '',
              type: 'log_line',
            }])
          }
        }
        return cc
      })
      break
    case 'event':
      runEvents.update(events => {
        const id = msg.data.run_id
        if (!events[id]) events[id] = []
        events[id].push(msg.data)
        return { ...events }
      })
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
      refreshLivePreviews()
      break
    case 'task_stats':
      taskStats.set(msg.data)
      break
    case 'new_comment':
      // Watcher detected a new comment file change.
      // Data has entity_kind + entity_id but not the comment itself.
      // Reload the conversation to get the new comment.
      if (msg.data && msg.data.entity_kind && msg.data.entity_id) {
        // Check if this matches the currently active conversation
        currentChat.update(cc => {
          if (cc.type === msg.data.entity_kind && cc.id === msg.data.entity_id) {
            // Reload conversation to get the new comment
            loadConversation(cc.type, cc.id)
          }
          return cc
        })
      }
      break
    case 'tree_update':
      // Watcher detected a task/project/run change — reload tree
      loadTree()
      // Also refresh live previews
      loadLivePreviews()
      break
    // Controller/project agent streaming — messages have text at top level.
    // Route to the correct agentStream based on which agent is active.
    case 'output':
      appendToActiveStream(msg, 'agent')
      break
    case 'error':
      appendToActiveStream(msg, 'system')
      break
    case 'user_message':
      appendToActiveStream(msg, 'human')
      break
    case 'started':
      appendToActiveStream(msg, 'system', `Agent started (PID: ${msg.pid || msg.data?.pid || '?'})`)
      setAgentRunning(true)
      break
    case 'ended':
      appendToActiveStream(msg, 'system', `Agent ended (exit: ${msg.exit || msg.data?.exit || '?'})`)
      setAgentRunning(false)
      break
    // Project agent streaming — same format as controller but on "project:{id}" channel.
    // The output/started/ended/error types are the same, so they're already handled above.
    // The only difference: project agent messages arrive on a different WS channel,
    // but handleMessage doesn't filter by channel — it processes all messages.
    // The channel filtering is done by the Hub (only delivers to subscribed clients).
  }
}

// ─── Live previews refresh ───
let previewTimer = null
function refreshLivePreviews() {
  // Debounce
  if (previewTimer) return
  previewTimer = setTimeout(async () => {
    previewTimer = null
    try {
      const data = await apiGet('/live')
      livePreviews.set(data.previews || [])
    } catch (e) { /* ignore */ }
  }, 500)
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

// ─── Channel subscription helpers ───

// Exported for ChatView to append human/system messages directly
export function appendToStream(nodeKey, msg) {
  agentStreams.update(streams => {
    if (!streams[nodeKey]) streams[nodeKey] = []
    streams[nodeKey] = [...streams[nodeKey], msg]
    return { ...streams }
  })
}

// Set agent running state for a node
function setAgentRunning(running) {
  const cc = get(currentChat)
  let nodeKey
  if (cc.type === 'workspace') nodeKey = 'workspace'
  else if (cc.type === 'project') nodeKey = 'project:' + cc.id
  else return
  agentRunning.update(states => {
    states[nodeKey] = running
    return { ...states }
  })
}

// Exported for ChatView to set running state on launch
export function setAgentRunningForKey(nodeKey, running) {
  agentRunning.update(states => {
    states[nodeKey] = running
    return { ...states }
  })
}

// Determine which node key an agent message belongs to.
// Messages from controller have no project_id → workspace.
// Messages from project agent have project_id → project:{id}.
function appendToActiveStream(msg, role, overrideBody) {
  const cc = get(currentChat)
  // Determine node key: workspace or project:{id}
  // If msg has project_id → project stream
  // Otherwise → current chat's stream (workspace or project)
  let nodeKey
  if (msg.project_id) {
    nodeKey = 'project:' + msg.project_id
  } else if (cc.type === 'workspace') {
    nodeKey = 'workspace'
  } else if (cc.type === 'project') {
    nodeKey = 'project:' + cc.id
  } else {
    nodeKey = 'workspace' // fallback
  }

  const body = overrideBody || msg.text || (msg.data?.text) || ''
  const ts = msg.ts || (msg.data?.ts) || ''

  agentStreams.update(streams => {
    if (!streams[nodeKey]) streams[nodeKey] = []
    streams[nodeKey] = [...streams[nodeKey], { role, body, timestamp: ts, type: msg.type }]
    return { ...streams }
  })
}

// Subscribe to a conversation channel to get new_comment pushes
export function subscribeConversation(type, id) {
  sendWS({ action: 'subscribe', channel: `conversation:${type}:${id}` })
}

export function unsubscribeConversation(type, id) {
  sendWS({ action: 'unsubscribe', channel: `conversation:${type}:${id}` })
}

// Subscribe to a run channel to get log_line + event pushes
export function subscribeRun(runId) {
  if (runId) sendWS({ action: 'subscribe', channel: `run:${runId}` })
}

export function unsubscribeRun(runId) {
  if (runId) sendWS({ action: 'unsubscribe', channel: `run:${runId}` })
}

// Subscribe to tree channel for streaming status updates
export function subscribeTree() {
  sendWS({ action: 'subscribe', channel: 'tree' })
}

// Subscribe to "runs" channel — receives all run_update, log_line, run_done events.
// The handler in handleMessage filters by currentChat to decide what to show.
export function subscribeRuns() {
  sendWS({ action: 'subscribe', channel: 'runs' })
}

// ─── Chat helpers ───
export async function loadConversation(type, id) {
  try {
    const data = await apiGet(`/conversations/${type}/${id}`)
    conversation.set(data.messages || [])
    currentChat.set({ type, id })
  } catch (e) {
    console.error('loadConversation:', e)
    conversation.set([])
  }
}

export async function loadTree() {
  try {
    const data = await apiGet('/tree')
    tree.set(data)
  } catch (e) {
    console.error('loadTree:', e)
  }
}

export async function sendChatMessage(type, id, body, author = 'human') {
  try {
    const c = await apiPost(`/conversations/${type}/${id}`, { author, body })
    // Also append locally for instant feedback
    conversation.update(msgs => [...msgs, {
      id: c.id,
      role: author,
      author,
      body,
      timestamp: c.created_at,
    }])
    return c
  } catch (e) {
    console.error('sendChatMessage:', e)
    return null
  }
}

export async function loadLivePreviews() {
  try {
    const data = await apiGet('/live')
    livePreviews.set(data.previews || [])
  } catch (e) { /* ignore */ }
}

export async function loadControllerHistory() {
  // Try structured session API first (state.db), fall back to log file
  try {
    const data = await apiGet('/session/hw_agent_controller/latest')
    const msgs = (data.messages || []).map(m => ({
      id: m.ID || m.id,
      role: m.Role || m.role || 'agent',
      content: m.Content || m.content || '',
      toolName: m.ToolName || m.tool_name || '',
      toolCalls: m.ToolCalls || m.tool_calls || [],
      timestamp: m.Timestamp || m.timestamp || '',
    }))
    if (msgs.length > 0) {
      sessionMessages.update(streams => {
        streams['workspace'] = msgs
        return { ...streams }
      })
      return
    }
  } catch (e) { /* fall through to log-based history */ }

  // Fallback: load from controller log file
  try {
    const data = await apiGet('/controller/history')
    const msgs = (data.messages || []).map(m => ({
      id: 0,
      role: m.role || 'agent',
      content: m.body || '',
      toolName: '',
      toolCalls: [],
      timestamp: m.timestamp || '',
    }))
    agentStreams.update(streams => {
      streams['workspace'] = msgs
      return { ...streams }
    })
  } catch (e) {
    console.error('loadControllerHistory:', e)
  }
}

export async function loadProjectAgentHistory(projectId) {
  // Try structured session API first
  try {
    const data = await apiGet(`/session/hw_agent_project/latest`)
    const msgs = (data.messages || []).map(m => ({
      id: m.ID || m.id,
      role: m.Role || m.role || 'agent',
      content: m.Content || m.content || '',
      toolName: m.ToolName || m.tool_name || '',
      toolCalls: m.ToolCalls || m.tool_calls || [],
      timestamp: m.Timestamp || m.timestamp || '',
    }))
    if (msgs.length > 0) {
      sessionMessages.update(streams => {
        streams['project:' + projectId] = msgs
        return { ...streams }
      })
      return
    }
  } catch (e) { /* fall through */ }

  // Fallback: log file
  try {
    const data = await apiGet(`/project-agent/${projectId}/history`)
    const msgs = (data.messages || []).map(m => ({
      id: 0,
      role: m.role || 'agent',
      content: m.body || '',
      toolName: '',
      toolCalls: [],
      timestamp: m.timestamp || '',
    }))
    agentStreams.update(streams => {
      streams['project:' + projectId] = msgs
      return { ...streams }
    })
  } catch (e) {
    console.error('loadProjectAgentHistory:', e)
  }
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