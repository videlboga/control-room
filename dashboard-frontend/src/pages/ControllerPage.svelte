<script>
import { apiGet, apiPost } from '../lib/stores.js'

let { onNavigate } = $props()

let messages = $state([])
let inputText = $state('')
let running = $state(false)
let epicId = $state('')
let epics = $state([])
let ws = $state(null)
let loading = $state(true)
let statusText = $state('')

async function load() {
  try {
    epics = await apiGet('/epics')
    const st = await apiGet('/controller/status')
    running = st.running || false
    if (running) {
      statusText = `Running (PID: ${st.pid})`
      connectWS()
    }
  } catch(e) { console.error(e) }
  loading = false
}
load()

function connectWS() {
  if (ws) return
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/ws`)
  ws.onopen = () => {
    ws.send(JSON.stringify({ action: 'subscribe', channel: 'controller' }))
  }
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.channel !== 'controller' && msg.type === 'snapshot') return
      handleWSMessage(msg)
    } catch(e) {}
  }
  ws.onclose = () => { ws = null }
}

function handleWSMessage(msg) {
  switch (msg.type) {
    case 'started':
      running = true
      statusText = `Running (PID: ${msg.pid || '?'})`
      messages.push({ role: 'system', text: `Controller started for epic ${msg.epic_id}`, ts: msg.ts || new Date().toISOString() })
      break
    case 'output':
      messages.push({ role: 'agent', text: msg.text, ts: msg.ts })
      break
    case 'error':
      messages.push({ role: 'error', text: msg.text, ts: msg.ts })
      break
    case 'user_message':
      messages.push({ role: 'user', text: msg.text, ts: msg.ts })
      break
    case 'ended':
      running = false
      statusText = `Ended (exit: ${msg.exit})`
      messages.push({ role: 'system', text: `Controller finished (exit code ${msg.exit})`, ts: new Date().toISOString() })
      break
    case 'stopped':
      running = false
      statusText = 'Stopped'
      messages.push({ role: 'system', text: 'Controller stopped', ts: new Date().toISOString() })
      break
  }
  messages = messages
}

async function launch() {
  if (!epicId) return
  messages = []
  try {
    const res = await apiPost('/controller/launch', { epic_id: epicId, prompt: '' })
    running = true
    statusText = `Running (PID: ${res.pid})`
    messages.push({ role: 'system', text: `Controller launched for epic ${epicId}`, ts: new Date().toISOString() })
    connectWS()
  } catch(e) {
    statusText = 'Error: ' + e.message
  }
}

async function send() {
  if (!inputText.trim() || !running) return
  const text = inputText.trim()
  inputText = ''
  try {
    await apiPost('/controller/send', { message: text })
  } catch(e) {
    messages.push({ role: 'error', text: e.message, ts: new Date().toISOString() })
  }
}

async function stop() {
  try {
    await apiPost('/controller/stop', {})
    running = false
    statusText = 'Stopped'
  } catch(e) { console.error(e) }
}

function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

let canSend = $derived(running && inputText.trim().length > 0)
</script>

<div class="controller-page">
  <div class="controller-header">
    <div class="header-left">
      <div class="page-title">Controller Agent</div>
      <div class="status-pill" class:running class:idle={!running}>
        {statusText || 'Idle'}
      </div>
    </div>
    <div class="header-right">
      {#if !running}
        <select bind:value={epicId} class="epic-select">
          <option value="">Select epic…</option>
          {#each epics as e}
            <option value={e.id}>{e.title || e.id}</option>
          {/each}
        </select>
        <button type="button" class="btn btn-launch" onclick={launch} disabled={!epicId}>
          Launch
        </button>
      {:else}
        <button type="button" class="btn btn-stop" onclick={stop}>Stop</button>
      {/if}
    </div>
  </div>

  <div class="chat-area">
    {#if loading}
      <div class="loading">Loading…</div>
    {:else if messages.length === 0}
      <div class="empty-chat">
        Select an epic and launch the controller agent to start managing your pipeline.
        Output will stream here in real-time.
      </div>
    {:else}
      {#each messages as m}
        <div class="msg msg-{m.role}">
          <div class="msg-meta">
            <span class="msg-role">{m.role}</span>
            <span class="msg-time">{formatTime(m.ts)}</span>
          </div>
          <div class="msg-text">{m.text}</div>
        </div>
      {/each}
    {/if}
  </div>

  <div class="input-area">
    <textarea
      bind:value={inputText}
      placeholder={running ? "Send a message to controller…" : "Launch controller first…"}
      class="chat-input"
      disabled={!running}
      onkeydown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }}
    ></textarea>
    <button type="button" class="btn btn-send" onclick={send} disabled={!canSend}>Send</button>
  </div>
</div>

<style>
.controller-page { display: flex; flex-direction: column; height: 100%; gap: 0; }
.controller-header { display: flex; align-items: center; justify-content: space-between; padding: 0 0 16px; border-bottom: 1px solid var(--border-subtle); margin-bottom: 16px; }
.header-left { display: flex; align-items: center; gap: 16px; }
.page-title { font-size: 20px; font-weight: 600; }
.status-pill { font-size: 12px; font-weight: 500; padding: 4px 12px; border-radius: var(--radius-pill); }
.status-pill.running { background: rgba(34,197,94,0.15); color: var(--success-bright); }
.status-pill.idle { background: rgba(255,255,255,0.05); color: var(--text-quaternary); }
.header-right { display: flex; align-items: center; gap: 8px; }
.epic-select { background: rgba(255,255,255,0.03); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 6px 12px; font-size: 13px; color: var(--text-primary); font-family: var(--font-sans); }
.btn { padding: 8px 16px; border-radius: var(--radius-sm); font-size: 14px; font-weight: 500; cursor: pointer; font-family: var(--font-sans); border: 1px solid; transition: all 0.12s; }
.btn:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-launch { background: rgba(245,166,35,0.15); border-color: rgba(245,166,35,0.3); color: var(--warning); }
.btn-launch:hover:not(:disabled) { background: rgba(245,166,35,0.25); }
.btn-stop { background: rgba(239,68,68,0.1); border-color: rgba(239,68,68,0.3); color: var(--danger); }
.btn-stop:hover { background: rgba(239,68,68,0.2); }
.btn-send { background: var(--accent); color: #fff; border: none; }
.btn-send:hover:not(:disabled) { background: var(--accent-hover); }

.chat-area { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; padding: 0 0 16px; }
.empty-chat { padding: 60px 24px; text-align: center; color: var(--text-quaternary); font-size: 14px; line-height: 1.6; }
.loading { padding: 60px; text-align: center; color: var(--text-quaternary); }

.msg { padding: 10px 14px; border-radius: var(--radius-md); max-width: 85%; }
.msg-agent { background: rgba(94,106,210,0.08); border: 1px solid rgba(94,106,210,0.15); align-self: flex-start; }
.msg-user { background: rgba(94,106,210,0.15); border: 1px solid rgba(94,106,210,0.25); align-self: flex-end; }
.msg-system { background: rgba(255,255,255,0.03); border: 1px solid var(--border-subtle); align-self: center; font-size: 13px; color: var(--text-tertiary); }
.msg-error { background: rgba(239,68,68,0.08); border: 1px solid rgba(239,68,68,0.15); align-self: flex-start; }
.msg-meta { display: flex; gap: 8px; margin-bottom: 4px; }
.msg-role { font-size: 11px; font-weight: 600; text-transform: uppercase; color: var(--text-quaternary); }
.msg-time { font-size: 10px; color: var(--text-quaternary); font-variant-numeric: tabular-nums; }
.msg-text { font-size: 13px; color: var(--text-secondary); line-height: 1.5; white-space: pre-wrap; word-break: break-word; font-family: var(--font-mono); }
.msg-system .msg-text { font-family: var(--font-sans); }

.input-area { display: flex; gap: 8px; padding: 12px 0 0; border-top: 1px solid var(--border-subtle); }
.chat-input { flex: 1; background: rgba(255,255,255,0.03); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 10px 12px; font-size: 14px; color: var(--text-primary); font-family: var(--font-sans); resize: none; min-height: 40px; max-height: 120px; }
.chat-input:focus { outline: none; border-color: var(--accent); }
.chat-input:disabled { opacity: 0.4; }
</style>