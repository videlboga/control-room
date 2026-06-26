<script>
import { apiGet, apiPost, formatTime, formatDuration } from '../lib/stores.js'
import Avatar from '../components/Avatar.svelte'

let { runId, onNavigate } = $props()

let run = $state(null)
let events = $state([])
let agentLog = $state([])
let comments = $state([])
let loading = $state(true)
let newComment = $state('')
let sendingComment = $state(false)
let redoStatus = $state('')

async function load() {
  try {
    const [runData, eventsData, logData, commentsData] = await Promise.all([
      apiGet(`/runs/${runId}`),
      apiGet(`/runs/${runId}/events?n=100`),
      apiGet(`/runs/${runId}/agent-log?n=200`),
      apiGet(`/runs/${runId}/comments`),
    ])
    run = runData.run || runData
    events = eventsData.events || eventsData || []
    agentLog = logData || []
    comments = commentsData || []
  } catch(e) { console.error(e) }
  loading = false
}
load()

let pct = $derived(run ? Math.round((run.tool_use_count || 0) / 200 * 100) : 0)
let isDone = $derived(run && (run.status === 'done' || run.status === 'failed'))

async function sendComment() {
  if (!newComment.trim()) return
  sendingComment = true
  try {
    const c = await apiPost(`/runs/${runId}/comments`, { author: 'human', body: newComment })
    comments = [...comments, c]
    newComment = ''
  } catch(e) { console.error(e) }
  sendingComment = false
}

async function triggerRedo() {
  redoStatus = 'Launching…'
  try {
    const res = await apiPost(`/runs/${runId}/redo`, {})
    redoStatus = `Redo started (PID: ${res.pid})`
  } catch(e) { redoStatus = 'Error: ' + e.message }
}

function eventIcon(type) {
  const icons = { tool_call: '💻', error: '⚠', step: '⚙️', info: '┊' }
  return icons[type] || '┊'
}
</script>

{#if loading}
  <div class="loading">Loading…</div>
{:else if run}
  <!-- Header -->
  <div class="detail-header">
    <button type="button" class="back-btn" onclick={() => onNavigate('runs')}>← Runs</button>
    <div class="titles">
      <div class="run-id">{run.id}</div>
      <div class="subtitle">{run.agent} · {run.step} · {run.project_id}</div>
    </div>
    <div class="header-right">
      <Avatar agent={run.agent} {pct} redo={run.redo_index || 0} />
    </div>
  </div>

  <!-- Meta grid -->
  <div class="meta-grid">
    <div class="meta-item"><div class="meta-label">Status</div><div class="meta-value {run.status === 'done' ? 'success' : run.status === 'failed' ? 'danger' : ''}">{run.status}</div></div>
    <div class="meta-item"><div class="meta-label">Verdict</div><div class="meta-value {run.verdict === 'approve' ? 'success' : run.verdict === 'reject' ? 'danger' : ''}">{run.verdict || '—'}</div></div>
    <div class="meta-item"><div class="meta-label">Tools</div><div class="meta-value accent">{run.tool_use_count || 0}</div></div>
    <div class="meta-item"><div class="meta-label">Redo</div><div class="meta-value">{run.redo_index || 0}</div></div>
    <div class="meta-item"><div class="meta-label">Duration</div><div class="meta-value">{formatDuration(run.started_at, run.ended_at)}</div></div>
    <div class="meta-item"><div class="meta-label">Started</div><div class="meta-value">{formatTime(run.started_at)}</div></div>
  </div>

  {#if run.verdict_reason}
    <div class="verdict-box verdict-{run.verdict}">
      <div class="verdict-label">Verdict Reason</div>
      <div class="verdict-text">{run.verdict_reason}</div>
    </div>
  {/if}

  {#if isDone}
    <div class="action-bar">
      <button type="button" class="action-btn redo-btn" onclick={triggerRedo}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2"><path d="M2 8a6 6 0 0 1 10.5-3.5M14 8a6 6 0 0 1-10.5 3.5"/><path d="M12 2v3h-3M4 14v-3h3"/></svg>
        Trigger Redo
      </button>
      {#if redoStatus}<span class="redo-status">{redoStatus}</span>{/if}
    </div>
  {/if}

  <!-- Chat timeline -->
  <div class="chat-section">
    <div class="section-title">Timeline & Logs</div>
    <div class="chat-list">
      {#each events as ev}
        <div class="chat-item agent-item">
          <div class="chat-avatar">{eventIcon(ev.type)}</div>
          <div class="chat-content">
            <div class="chat-meta">
              <span class="chat-author">{ev.agent || 'system'}</span>
              <span class="chat-time">{formatTime(ev.timestamp)}</span>
            </div>
            <div class="chat-body">
              {#if ev.tool}<span class="tool-name">{ev.tool}</span>{/if}
              <span class="event-type">{ev.type}</span>
              {#if ev.payload}<pre class="event-payload">{ev.payload}</pre>{/if}
            </div>
          </div>
        </div>
      {/each}

      {#if agentLog.length > 0}
        <div class="chat-item log-item">
          <div class="chat-avatar">📄</div>
          <div class="chat-content">
            <div class="chat-meta">
              <span class="chat-author">agent.log</span>
              <span class="chat-time">{agentLog.length} lines</span>
            </div>
            <pre class="full-log">{agentLog.join('\n')}</pre>
          </div>
        </div>
      {/if}
    </div>
  </div>

  <!-- Comments / Chat -->
  <div class="chat-section">
    <div class="section-title">Comments</div>
    <div class="comments-list">
      {#each comments as c}
        <div class="chat-item comment-item">
          <div class="chat-avatar author-{c.author}">{(c.author || '?')[0].toUpperCase()}</div>
          <div class="chat-content">
            <div class="chat-meta">
              <span class="chat-author">{c.author}</span>
              <span class="chat-time">{formatTime(c.created_at)}</span>
            </div>
            <div class="chat-body comment-body">{c.body}</div>
          </div>
        </div>
      {/each}
      {#if comments.length === 0}
        <div class="no-comments">No comments yet</div>
      {/if}
    </div>
    <div class="comment-input">
      <textarea bind:value={newComment} placeholder="Write a comment…" class="comment-textarea" onkeydown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendComment() } }}></textarea>
      <button type="button" class="send-btn" onclick={sendComment} disabled={sendingComment || !newComment.trim()}>Send</button>
    </div>
  </div>
{:else}
  <div class="loading">Run not found</div>
{/if}

<style>
.detail-header { display: flex; align-items: center; gap: 16px; margin-bottom: 24px; }
.back-btn { font-size: 14px; color: var(--text-tertiary); padding: 6px 12px; border-radius: var(--radius-sm); border: 1px solid var(--border-subtle); background: none; cursor: pointer; font-family: var(--font-sans); transition: all 0.12s; flex-shrink: 0; }
.back-btn:hover { color: var(--text-secondary); border-color: var(--border); }
.titles { flex: 1; }
.run-id { font-size: 22px; font-weight: 600; letter-spacing: -0.44px; }
.subtitle { font-size: 14px; color: var(--text-tertiary); margin-top: 4px; }
.header-right { margin-left: auto; }

.meta-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 12px; margin-bottom: 20px; }
.meta-item { background: rgba(255,255,255,0.02); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 12px 16px; }
.meta-label { font-size: 11px; font-weight: 500; color: var(--text-quaternary); text-transform: uppercase; letter-spacing: 0.3px; margin-bottom: 6px; }
.meta-value { font-size: 15px; font-weight: 600; color: var(--text-primary); font-variant-numeric: tabular-nums; }
.meta-value.success { color: var(--success-bright); }
.meta-value.danger { color: var(--danger); }
.meta-value.accent { color: var(--accent-bright); }

.verdict-box { padding: 16px; border-radius: var(--radius-md); margin-bottom: 20px; border: 1px solid var(--border-subtle); }
.verdict-box.verdict-approve { border-left: 3px solid var(--success-bright); }
.verdict-box.verdict-reject { border-left: 3px solid var(--danger); }
.verdict-label { font-size: 12px; font-weight: 600; color: var(--text-quaternary); text-transform: uppercase; letter-spacing: 0.3px; margin-bottom: 8px; }
.verdict-text { font-size: 14px; color: var(--text-tertiary); line-height: 1.5; }

.action-bar { display: flex; align-items: center; gap: 12px; margin-bottom: 24px; }
.action-btn { padding: 8px 16px; border-radius: var(--radius-sm); font-size: 14px; font-weight: 500; cursor: pointer; font-family: var(--font-sans); border: 1px solid; display: flex; align-items: center; gap: 8px; transition: all 0.12s; }
.redo-btn { background: rgba(94,106,210,0.1); border-color: rgba(94,106,210,0.3); color: var(--accent-bright); }
.redo-btn:hover { background: rgba(94,106,210,0.2); border-color: var(--accent); }
.redo-status { font-size: 13px; color: var(--text-tertiary); }

.chat-section { margin-bottom: 24px; }
.section-title { font-size: 14px; font-weight: 600; color: var(--text-secondary); margin-bottom: 12px; }
.chat-list { display: flex; flex-direction: column; gap: 2px; max-height: 500px; overflow-y: auto; }
.chat-item { display: flex; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--border-subtle); }
.chat-item:last-child { border-bottom: none; }
.chat-avatar { width: 32px; height: 32px; border-radius: var(--radius-sm); background: var(--bg-elevated); display: flex; align-items: center; justify-content: center; font-size: 14px; flex-shrink: 0; }
.chat-avatar.author-human { background: rgba(94,106,210,0.15); color: var(--accent-bright); font-weight: 600; }
.chat-avatar.author-system { background: rgba(245,166,35,0.15); }
.chat-content { flex: 1; min-width: 0; }
.chat-meta { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.chat-author { font-size: 13px; font-weight: 600; color: var(--text-secondary); }
.chat-time { font-size: 11px; color: var(--text-quaternary); font-variant-numeric: tabular-nums; }
.chat-body { font-size: 13px; color: var(--text-tertiary); line-height: 1.5; }
.tool-name { font-family: var(--font-mono); font-size: 12px; color: var(--accent-bright); margin-right: 6px; }
.event-type { font-size: 11px; color: var(--text-quaternary); text-transform: uppercase; }
.event-payload { font-family: var(--font-mono); font-size: 12px; color: var(--text-tertiary); margin-top: 6px; padding: 8px; background: var(--bg-panel); border-radius: var(--radius-sm); white-space: pre-wrap; word-break: break-all; max-height: 200px; overflow-y: auto; }

.log-item pre.full-log { font-family: var(--font-mono); font-size: 12px; line-height: 1.7; color: var(--text-tertiary); white-space: pre-wrap; word-break: break-all; padding: 12px; background: var(--bg-panel); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); max-height: 400px; overflow-y: auto; margin-top: 6px; }

.comments-list { display: flex; flex-direction: column; gap: 2px; margin-bottom: 12px; }
.comment-item { border-bottom: 1px solid var(--border-subtle); }
.comment-body { color: var(--text-secondary); }
.no-comments { padding: 20px; text-align: center; color: var(--text-quaternary); font-size: 14px; }

.comment-input { display: flex; gap: 8px; }
.comment-textarea { flex: 1; background: rgba(255,255,255,0.03); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 10px 12px; font-size: 14px; color: var(--text-primary); font-family: var(--font-sans); resize: none; min-height: 40px; max-height: 120px; }
.comment-textarea:focus { outline: none; border-color: var(--accent); }
.send-btn { padding: 8px 16px; background: var(--accent); color: #fff; border: none; border-radius: var(--radius-sm); font-size: 14px; font-weight: 500; cursor: pointer; font-family: var(--font-sans); }
.send-btn:hover:not(:disabled) { background: var(--accent-hover); }
.send-btn:disabled { opacity: 0.4; cursor: not-allowed; }

.loading { padding: 80px 24px; text-align: center; color: var(--text-quaternary); }
</style>