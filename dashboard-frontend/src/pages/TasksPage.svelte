<script>
import { apiGet } from '../lib/stores.js'

let { onNavigate } = $props()

let tasks = $state([])
let loading = $state(true)

async function loadTasks() {
  try { tasks = await apiGet('/tasks') } catch(e) { console.error(e) }
  loading = false
}
loadTasks()

const statuses = ['open', 'in_progress', 'pending_review', 'approved', 'rejected', 'done']

let filterProject = $state('')
let filterType = $state('')
let filterStatus = $state('')

let filtered = $derived(
  tasks.filter(t => {
    if (filterProject && t.project_id !== filterProject) return false
    if (filterType && t.type !== filterType) return false
    if (filterStatus && t.status !== filterStatus) return false
    return true
  })
)

function redoClass(r) {
  if (r <= 3) return 'low'; if (r <= 7) return 'mid'; return 'high'
}
</script>

<div class="tasks-page">
  <div class="filters">
    <select bind:value={filterStatus} class="filter-select">
      <option value="">All statuses</option>
      {#each statuses as s}<option value={s}>{s.replace('_',' ')}</option>{/each}
    </select>
    <select bind:value={filterType} class="filter-select">
      <option value="">All types</option>
      <option value="research">research</option>
      <option value="qa_review">qa_review</option>
      <option value="pm_plan">pm_plan</option>
      <option value="engineering">engineering</option>
      <option value="qa_verify">qa_verify</option>
      <option value="pm_consistency">pm_consistency</option>
    </select>
    <span class="count">{filtered.length} tasks</span>
  </div>

  {#if loading}
    <div class="loading">Loading…</div>
  {:else}
    <div class="task-list">
      {#each filtered as task (task.id)}
        <div class="task-card" role="button" tabindex="0" onclick={() => onNavigate('task-detail', task.id)} onkeydown={(e) => e.key === 'Enter' && onNavigate('task-detail', task.id)}>
          <div class="task-title">{task.title}</div>
          <div class="task-meta">
            <span class="type-badge {task.type}">{task.type.replace('_',' ')}</span>
            <span class="task-id">{task.display_id || task.id}</span>
            <span class="status-badge {task.status}">{task.status.replace('_',' ')}</span>
            {#if task.redo_index > 0}
              <span class="redo-badge {redoClass(task.redo_index)}">redo {task.redo_index}</span>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
.tasks-page { display: flex; flex-direction: column; gap: 16px; }
.filters { display: flex; gap: 12px; align-items: center; }
.filter-select { background: var(--bg-elevated); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 6px 12px; font-size: 13px; color: var(--text-secondary); font-family: var(--font-sans); }
.filter-select:focus { outline: none; border-color: var(--accent); }
.count { font-size: 13px; color: var(--text-quaternary); margin-left: auto; }
.loading { padding: 40px; text-align: center; color: var(--text-quaternary); }
.task-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(400px, 1fr)); gap: 12px; }
.task-card { background: rgba(255,255,255,0.02); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 14px; transition: all 0.12s; cursor: pointer; }
.task-card:hover { border-color: rgba(255,255,255,0.12); background: rgba(255,255,255,0.04); }
.task-title { font-size: 13px; font-weight: 500; color: var(--text-primary); margin-bottom: 8px; line-height: 1.4; }
.task-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
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
</style>