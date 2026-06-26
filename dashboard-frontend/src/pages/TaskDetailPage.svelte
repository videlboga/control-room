<script>
import { apiGet, formatTime, formatDuration } from '../lib/stores.js'

let { taskId, onNavigate } = $props()

let task = $state(null)
let runs = $state([])
let comments = $state([])
let loading = $state(true)

async function load() {
  try {
    const [taskData, commentsData] = await Promise.all([
      apiGet(`/tasks/${taskId}`),
      apiGet(`/tasks/${taskId}/comments`),
    ])
    task = taskData
    comments = commentsData || []
    // Fetch runs for this task
    runs = await apiGet(`/tasks/${taskId}/runs`)
  } catch(e) { console.error(e) }
  loading = false
}
load()

function redoClass(r) {
  if (r <= 3) return 'low'; if (r <= 7) return 'mid'; return 'high'
}
</script>

{#if loading}
  <div class="loading">Loading…</div>
{:else if task}
  <div class="detail-header">
    <a href="#" class="back-link" onclick={(e) => { e.preventDefault(); onNavigate('tasks') }}>← Tasks</a>
    <div class="titles">
      <div class="task-title">{task.title}</div>
      <div class="task-subtitle">
        <span class="type-badge {task.type}">{task.type.replace('_',' ')}</span>
        <span class="task-id">{task.display_id || task.id}</span>
        <span class="status-badge {task.status}">{task.status}</span>
        {#if task.redo_index > 0}
          <span class="redo-badge {redoClass(task.redo_index)}">redo {task.redo_index}</span>
        {/if}
      </div>
    </div>
  </div>

  <div class="meta-grid">
    <div class="meta-item"><div class="meta-label">Project</div><div class="meta-value">{task.project_id}</div></div>
    <div class="meta-item"><div class="meta-label">Team</div><div class="meta-value">{task.team_id}</div></div>
    <div class="meta-item"><div class="meta-label">Epic</div><div class="meta-value">{task.epic_id || '—'}</div></div>
    <div class="meta-item"><div class="meta-label">Status</div><div class="meta-value">{task.status}</div></div>
    <div class="meta-item"><div class="meta-label">Verdict</div><div class="meta-value">{task.verdict || '—'}</div></div>
    <div class="meta-item"><div class="meta-label">Created</div><div class="meta-value">{formatTime(task.created_at)}</div></div>
  </div>

  {#if task.description}
    <div class="section">
      <div class="section-title">Description</div>
      <div class="section-body">{task.description}</div>
    </div>
  {/if}

  {#if task.verdict_reason}
    <div class="section">
      <div class="section-title">Verdict Reason</div>
      <div class="section-body verdict-{task.verdict}">{task.verdict_reason}</div>
    </div>
  {/if}

  {#if runs.length > 0}
    <div class="section">
      <div class="section-title">Runs ({runs.length})</div>
      <div class="runs-list">
        {#each runs as r}
          <div class="run-row" role="button" tabindex="0" onclick={() => onNavigate('run-detail', r.id)} onkeydown={(e) => e.key === 'Enter' && onNavigate('run-detail', r.id)}>
            <div class="run-status-dot {r.status}"></div>
            <div class="run-info">
              <div class="run-id">{r.id}</div>
              <div class="run-meta">{r.agent} · {r.step} · {formatDuration(r.started_at, r.ended_at)}</div>
            </div>
            <div class="run-tools">{r.tool_use_count || 0} tools</div>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if comments.length > 0}
    <div class="section">
      <div class="section-title">Comments ({comments.length})</div>
      <div class="comments-list">
        {#each comments as c}
          <div class="comment">
            <div class="comment-author">{c.author}</div>
            <div class="comment-body">{c.body}</div>
            <div class="comment-time">{formatTime(c.created_at)}</div>
          </div>
        {/each}
      </div>
    </div>
  {/if}
{:else}
  <div class="loading">Task not found</div>
{/if}

<style>
.detail-header { display: flex; align-items: flex-start; gap: 16px; margin-bottom: 24px; }
.back-link { font-size: 14px; color: var(--text-tertiary); text-decoration: none; padding: 6px 12px; border-radius: var(--radius-sm); border: 1px solid var(--border-subtle); transition: all 0.12s; flex-shrink: 0; }
.back-link:hover { color: var(--text-secondary); border-color: var(--border); }
.titles { flex: 1; }
.task-title { font-size: 22px; font-weight: 600; letter-spacing: -0.44px; margin-bottom: 8px; }
.task-subtitle { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.type-badge { font-size: 10px; font-weight: 500; padding: 2px 8px; border-radius: var(--radius-pill); text-transform: uppercase; letter-spacing: 0.3px; background: rgba(255,255,255,0.05); color: var(--text-tertiary); }
.type-badge.engineering { background: rgba(94,106,210,0.1); color: var(--accent-bright); }
.type-badge.research { background: rgba(16,185,129,0.1); color: var(--success-bright); }
.type-badge.qa_review, .type-badge.qa_verify { background: rgba(245,166,35,0.1); color: var(--warning); }
.type-badge.pm_plan, .type-badge.pm_consistency { background: rgba(229,72,77,0.1); color: var(--danger); }
.task-id { font-size: 11px; color: var(--text-quaternary); font-family: var(--font-mono); }
.status-badge { font-size: 10px; padding: 2px 8px; border-radius: var(--radius-pill); }
.status-badge.open { background: rgba(255,255,255,0.05); color: var(--text-tertiary); }
.status-badge.in_progress { background: rgba(94,106,210,0.1); color: var(--accent-bright); }
.status-badge.pending_review { background: rgba(245,166,35,0.1); color: var(--warning); }
.status-badge.approved { background: rgba(16,185,129,0.1); color: var(--success-bright); }
.status-badge.rejected { background: rgba(229,72,77,0.1); color: var(--danger); }
.status-badge.done { background: rgba(255,255,255,0.03); color: var(--text-quaternary); }
.redo-badge { font-size: 10px; padding: 2px 7px; border-radius: var(--radius-pill); font-weight: 600; }
.redo-badge.low { background: rgba(39,166,68,0.1); color: var(--success-bright); }
.redo-badge.mid { background: rgba(245,166,35,0.1); color: var(--warning); }
.redo-badge.high { background: rgba(229,72,77,0.1); color: var(--danger); }
.meta-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 12px; margin-bottom: 24px; }
.meta-item { background: rgba(255,255,255,0.02); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 14px 16px; }
.meta-label { font-size: 11px; font-weight: 500; color: var(--text-quaternary); text-transform: uppercase; letter-spacing: 0.3px; margin-bottom: 6px; }
.meta-value { font-size: 15px; font-weight: 500; color: var(--text-primary); font-variant-numeric: tabular-nums; }
.section { margin-bottom: 24px; }
.section-title { font-size: 14px; font-weight: 600; color: var(--text-secondary); margin-bottom: 12px; }
.section-body { font-size: 14px; color: var(--text-tertiary); line-height: 1.6; padding: 16px; background: rgba(255,255,255,0.02); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); }
.section-body.verdict-approve { border-left: 3px solid var(--success-bright); }
.section-body.verdict-reject { border-left: 3px solid var(--danger); }
.runs-list { display: flex; flex-direction: column; gap: 8px; }
.run-row { display: flex; align-items: center; gap: 12px; padding: 12px; background: rgba(255,255,255,0.02); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); cursor: pointer; transition: all 0.12s; }
.run-row:hover { border-color: var(--border); background: rgba(255,255,255,0.04); }
.run-status-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.run-status-dot.running { background: var(--accent-bright); box-shadow: 0 0 6px rgba(113,112,255,0.5); }
.run-status-dot.done { background: var(--success-bright); }
.run-status-dot.failed { background: var(--danger); }
.run-info { flex: 1; }
.run-id { font-size: 13px; font-weight: 500; font-family: var(--font-mono); color: var(--text-secondary); }
.run-meta { font-size: 12px; color: var(--text-quaternary); margin-top: 2px; }
.run-tools { font-size: 12px; color: var(--text-tertiary); font-variant-numeric: tabular-nums; }
.comments-list { display: flex; flex-direction: column; gap: 10px; }
.comment { padding: 12px; background: rgba(255,255,255,0.02); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); }
.comment-author { font-size: 13px; font-weight: 600; color: var(--text-secondary); margin-bottom: 4px; }
.comment-body { font-size: 14px; color: var(--text-tertiary); line-height: 1.5; }
.comment-time { font-size: 11px; color: var(--text-quaternary); margin-top: 6px; }
.loading { padding: 80px 24px; text-align: center; color: var(--text-quaternary); }
</style>