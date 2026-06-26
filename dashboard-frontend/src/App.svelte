<script>
import { onMount } from 'svelte'
import { connectWS, activeRuns, taskStats } from './lib/stores.js'
import Sidebar from './components/Sidebar.svelte'
import RunCard from './components/RunCard.svelte'
import TopBar from './components/TopBar.svelte'
import BoardPage from './pages/BoardPage.svelte'
import TasksPage from './pages/TasksPage.svelte'
import TaskDetailPage from './pages/TaskDetailPage.svelte'
import RunDetailPage from './pages/RunDetailPage.svelte'
import SettingsPage from './pages/SettingsPage.svelte'

let page = $state('runs')
let selectedTaskId = $state('')
let selectedRunId = $state('')

const titles = {
  runs: 'Active Runs',
  board: 'Task Board',
  tasks: 'All Tasks',
  'task-detail': 'Task Details',
  'run-detail': 'Run Details',
  settings: 'Settings',
}

function navigate(p, id) {
  if (id) {
    if (p === 'task-detail') selectedTaskId = id
    if (p === 'run-detail') selectedRunId = id
  }
  page = p
}

onMount(() => { connectWS() })
</script>

<div class="app">
  <Sidebar {page} onNavigate={navigate} />
  <main class="main">
    <TopBar title={titles[page] || page} />
    <div class="content">
      {#if page === 'runs'}
        <div class="runs-grid">
          {#each $activeRuns as run (run.id)}
            <RunCard {run} onNavigate={navigate} />
          {/each}
          {#if $activeRuns.length === 0}
            <div class="empty">
              <div class="empty-icon">⏳</div>
              <div class="empty-title">No active runs</div>
              <div class="empty-desc">Agent runs will appear here when the orchestrator is active</div>
            </div>
          {/if}
        </div>
      {:else if page === 'board'}
        <BoardPage />
      {:else if page === 'tasks'}
        <TasksPage onNavigate={navigate} />
      {:else if page === 'task-detail'}
        <TaskDetailPage taskId={selectedTaskId} onNavigate={navigate} />
      {:else if page === 'run-detail'}
        <RunDetailPage runId={selectedRunId || 'run_a34152a2'} />
      {:else if page === 'settings'}
        <SettingsPage />
      {/if}
    </div>
  </main>
</div>

<style>
.app { display: flex; min-height: 100vh; }
.main { flex: 1; min-width: 0; }
.content { padding: 24px; }
.runs-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(440px, 1fr)); gap: 16px; }
.empty { text-align: center; padding: 80px 24px; color: var(--text-quaternary); grid-column: 1 / -1; }
.empty-icon { font-size: 48px; margin-bottom: 16px; opacity: 0.3; }
.empty-title { font-size: 18px; font-weight: 500; color: var(--text-tertiary); margin-bottom: 8px; }
.empty-desc { font-size: 14px; }
@media (max-width: 768px) { .runs-grid { grid-template-columns: 1fr; } }
</style>