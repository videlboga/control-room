<script>
import { apiGet } from '../lib/stores.js'

let tasks = $state([])
let loading = $state(true)

async function load() {
  try { tasks = await apiGet('/tasks') } catch(e) { console.error(e) }
  loading = false
}
load()

const statuses = ['open', 'in_progress', 'pending_review', 'approved', 'rejected', 'done']

let filterType = $state('')

function redoClass(r) {
  if (r <= 3) return 'low'; if (r <= 7) return 'mid'; return 'high'
}
</script>

<div class="board">
  {#each statuses as status}
    {@const colTasks = tasks.filter(t => t.status === status && (!filterType || t.type === filterType))}
    <div class="column">
      <div class="col-header">
        {status.replace('_',' ')}
        <span class="col-count">{colTasks.length}</span>
      </div>
      {#each colTasks as task (task.id)}
        <div class="task-card">
          <div class="task-title">{task.title}</div>
          <div class="task-meta">
            <span class="type-badge {task.type}">{task.type.replace('_',' ')}</span>
            {#if task.redo_index > 0}
              <span class="redo-badge {redoClass(task.redo_index)}">redo {task.redo_index}</span>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/each}
</div>

<style>
.board { display: flex; gap: 16px; overflow-x: auto; padding-bottom: 8px; }
.column { flex: 1; min-width: 260px; display: flex; flex-direction: column; gap: 10px; }
.col-header { display: flex; align-items: center; gap: 8px; padding: 8px 4px; font-size: 12px; font-weight: 600; color: var(--text-tertiary); letter-spacing: 0.3px; text-transform: uppercase; }
.col-count { font-size: 11px; padding: 1px 7px; border-radius: var(--radius-pill); background: rgba(255,255,255,0.06); color: var(--text-quaternary); }
.task-card { background: rgba(255,255,255,0.02); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 14px; transition: all 0.12s; }
.task-card:hover { border-color: rgba(255,255,255,0.12); background: rgba(255,255,255,0.04); }
.task-title { font-size: 13px; font-weight: 500; color: var(--text-primary); margin-bottom: 8px; line-height: 1.4; }
.task-meta { display: flex; align-items: center; gap: 8px; }
.type-badge { font-size: 10px; font-weight: 500; padding: 2px 8px; border-radius: var(--radius-pill); text-transform: uppercase; letter-spacing: 0.3px; background: rgba(255,255,255,0.05); color: var(--text-tertiary); }
.type-badge.engineering { background: rgba(94,106,210,0.1); color: var(--accent-bright); }
.type-badge.research { background: rgba(16,185,129,0.1); color: var(--success-bright); }
.type-badge.qa_review, .type-badge.qa_verify { background: rgba(245,166,35,0.1); color: var(--warning); }
.type-badge.pm_plan, .type-badge.pm_consistency { background: rgba(229,72,77,0.1); color: var(--danger); }
.redo-badge { font-size: 10px; padding: 2px 7px; border-radius: var(--radius-pill); font-weight: 600; }
.redo-badge.low { background: rgba(39,166,68,0.1); color: var(--success-bright); }
.redo-badge.mid { background: rgba(245,166,35,0.1); color: var(--warning); }
.redo-badge.high { background: rgba(229,72,77,0.1); color: var(--danger); }
</style>