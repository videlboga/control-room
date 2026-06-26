<script>
import { apiGet, apiPost } from '../lib/stores.js'

let tab = $state('agents')
let teams = $state([])
let projects = $state([])
let epics = $state([])
let loading = $state(true)

// New project form
let showNewProject = $state(false)
let newProj = $state({ id: '', title: '', repo: '', team: '', test: '', lint: '' })
let projError = $state('')
let projSuccess = $state('')

// New epic + launch form
let showNewEpic = $state(false)
let newEpic = $state({ title: '', description: '', project_id: '', team_id: '' })
let epicError = $state('')
let launchStatus = $state('')

async function load() {
  try {
    teams = await apiGet('/agents')
    projects = await apiGet('/projects')
    epics = await apiGet('/epics')
  } catch(e) { console.error(e) }
  loading = false
}
load()

const avatarLabels = [
  '0-30% · 0-3', '0-30% · 3-7', '0-30% · 7+',
  '30-70% · 0-3', '30-70% · 3-7', '30-70% · 7+',
  '70%+ · 0-3', '70%+ · 3-7', '70%+ · 7+',
]
const agents = ['researcher', 'engineer', 'qa', 'pm']

async function createProject() {
  projError = ''; projSuccess = ''
  if (!newProj.id || !newProj.title || !newProj.repo) {
    projError = 'ID, title and repo path are required'
    return
  }
  try {
    await apiPost('/projects', {
      id: newProj.id,
      title: newProj.title,
      repo_path: newProj.repo,
      default_team: newProj.team || 'agent-dashboard',
      test_command: newProj.test,
      lint_command: newProj.lint,
    })
    projSuccess = 'Project created'
    showNewProject = false
    newProj = { id: '', title: '', repo: '', team: '', test: '', lint: '' }
    await load()
  } catch(e) { projError = e.message }
}

async function createEpicAndLaunch() {
  epicError = ''; launchStatus = ''
  if (!newEpic.title || !newEpic.project_id) {
    epicError = 'Title and project are required'
    return
  }
  try {
    const epic = await apiPost('/epics', {
      title: newEpic.title,
      description: newEpic.description,
      project_id: newEpic.project_id,
    })
    launchStatus = `Epic created: ${epic.id}. Launching orchestrator…`
    // Launch orchestrator
    const res = await apiPost('/orchestrate', { epic_id: epic.id })
    launchStatus = `Epic ${epic.id} launched (PID: ${res.pid})`
    showNewEpic = false
    newEpic = { title: '', description: '', project_id: '', team_id: '' }
    await load()
  } catch(e) { epicError = e.message }
}
</script>

<div class="settings">
  <div class="tabs">
    {#each ['agents', 'avatars', 'projects', 'workspace', 'new'] as t}
      <button type="button" class="tab" class:active={tab === t} onclick={() => tab = t}>
        {#if t === 'new'}+ New{:else}{t}{/if}
      </button>
    {/each}
  </div>

  {#if tab === 'agents'}
    <div class="cards">
      {#each teams as team}
        <div class="setting-card">
          <div class="card-title">{team.name || team.id}</div>
          <div class="agent-list">
            {#each Object.entries(team.agents || {}) as [name, agent]}
              <div class="agent-row">
                <div class="agent-avatar">{name[0].toUpperCase()}</div>
                <div class="agent-info">
                  <div class="agent-name">{name}</div>
                  <div class="agent-profile">{agent.profile}</div>
                </div>
                <span class="role-badge">{agent.role}</span>
              </div>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {:else if tab === 'avatars'}
    {#each agents as agent}
      <div class="avatar-section">
        <div class="section-title">{agent} — 3×3 grid (tool-use × redo)</div>
        <div class="legend">
          ↔ Tool: <span class="green">0-30%</span> · <span class="orange">30-70%</span> · <span class="red">70%+</span>
          &nbsp; ↕ Redo: <span class="green">0-3</span> · <span class="orange">3-7</span> · <span class="red">7+</span>
        </div>
        <div class="avatar-grid">
          {#each avatarLabels as label}
            <div class="avatar-cell" title={label} role="button" tabindex="0">
              <div class="upload-label">+ GIF</div>
              <div class="cell-label">{label}</div>
            </div>
          {/each}
        </div>
      </div>
    {/each}
  {:else if tab === 'projects'}
    <div class="cards">
      {#each projects as p}
        <div class="setting-card">
          <div class="card-title">{p.title}</div>
          <div class="info-row"><span class="info-label">ID</span><span class="info-value mono">{p.id}</span></div>
          <div class="info-row"><span class="info-label">Repo</span><span class="info-value mono">{p.repo_path}</span></div>
          <div class="info-row"><span class="info-label">Team</span><span class="info-value">{p.default_team}</span></div>
          {#if p.test_command}
            <div class="info-row"><span class="info-label">Test</span><span class="info-value mono">{p.test_command}</span></div>
          {/if}
        </div>
      {/each}
    </div>
  {:else if tab === 'workspace'}
    <div class="cards">
      <div class="setting-card">
        <div class="card-title">Runtime</div>
        <div class="info-row"><span class="info-label">Model</span><span class="info-value mono">glm-5.2</span></div>
        <div class="info-row"><span class="info-label">Provider</span><span class="info-value mono">ollama-cloud</span></div>
        <div class="info-row"><span class="info-label">Max Concurrent</span><span class="info-value">4</span></div>
        <div class="info-row"><span class="info-label">Max Turns</span><span class="info-value">200</span></div>
      </div>
      <div class="setting-card">
        <div class="card-title">Policy</div>
        <div class="info-row"><span class="info-label">Max Redo</span><span class="info-value">5</span></div>
        <div class="info-row"><span class="info-label">Auto Approve</span><span class="info-value">24h</span></div>
      </div>
    </div>
  {:else if tab === 'new'}
    <div class="cards">
      <!-- Create Project -->
      <div class="setting-card">
        <div class="card-title">Create Project</div>
        <div class="form-field">
          <label>ID</label>
          <input bind:value={newProj.id} placeholder="my-project" class="form-input mono">
        </div>
        <div class="form-field">
          <label>Title</label>
          <input bind:value={newProj.title} placeholder="Project Title" class="form-input">
        </div>
        <div class="form-field">
          <label>Repo Path</label>
          <input bind:value={newProj.repo} placeholder="/tmp/my-repo" class="form-input mono">
        </div>
        <div class="form-field">
          <label>Default Team</label>
          <select bind:value={newProj.team} class="form-input">
            <option value="agent-dashboard">agent-dashboard</option>
          </select>
        </div>
        <div class="form-field">
          <label>Test Command <span class="optional">(optional — gate runs this to verify engineering work)</span></label>
          <input bind:value={newProj.test} placeholder="e.g. npm test, go test ./..., python -m pytest" class="form-input mono">
        </div>
        <div class="form-field">
          <label>Lint Command <span class="optional">(optional — gate runs this for code quality)</span></label>
          <input bind:value={newProj.lint} placeholder="e.g. eslint ., go vet ./..." class="form-input mono">
        </div>
        {#if projError}<div class="form-error">{projError}</div>{/if}
        {#if projSuccess}<div class="form-success">{projSuccess}</div>{/if}
        <button type="button" class="form-btn" onclick={createProject}>Create Project</button>
      </div>

      <!-- Create Epic + Launch -->
      <div class="setting-card">
        <div class="card-title">Create Epic & Launch</div>
        <div class="form-field">
          <label>Title</label>
          <input bind:value={newEpic.title} placeholder="Epic title / task description" class="form-input">
        </div>
        <div class="form-field">
          <label>Description</label>
          <textarea bind:value={newEpic.description} placeholder="Detailed task description for agents…" class="form-input form-textarea"></textarea>
        </div>
        <div class="form-field">
          <label>Project</label>
          <select bind:value={newEpic.project_id} class="form-input">
            <option value="">Select project…</option>
            {#each projects as p}<option value={p.id}>{p.title}</option>{/each}
          </select>
        </div>
        {#if epicError}<div class="form-error">{epicError}</div>{/if}
        {#if launchStatus}<div class="form-success">{launchStatus}</div>{/if}
        <button type="button" class="form-btn" onclick={createEpicAndLaunch}>Create & Launch</button>
      </div>
    </div>
  {/if}
</div>

<style>
.settings { display: flex; flex-direction: column; gap: 24px; }
.tabs { display: flex; gap: 4px; border-bottom: 1px solid var(--border-subtle); }
.tab { padding: 10px 16px; font-size: 14px; font-weight: 500; color: var(--text-tertiary); cursor: pointer; border: none; background: none; border-bottom: 2px solid transparent; transition: all 0.12s; text-transform: capitalize; font-family: var(--font-sans); }
.tab:hover { color: var(--text-secondary); }
.tab.active { color: var(--accent-bright); border-bottom-color: var(--accent-bright); }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(400px, 1fr)); gap: 16px; }
.setting-card { background: rgba(255,255,255,0.02); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: 20px; }
.card-title { font-size: 15px; font-weight: 600; margin-bottom: 16px; }
.agent-list { display: flex; flex-direction: column; gap: 10px; }
.agent-row { display: flex; align-items: center; gap: 12px; padding: 10px; background: rgba(255,255,255,0.02); border-radius: 6px; border: 1px solid var(--border-subtle); }
.agent-avatar { width: 36px; height: 36px; border-radius: var(--radius-sm); background: var(--bg-elevated); display: flex; align-items: center; justify-content: center; font-size: 16px; font-weight: 600; color: var(--text-secondary); }
.agent-info { flex: 1; }
.agent-name { font-size: 14px; font-weight: 500; }
.agent-profile { font-size: 12px; color: var(--text-quaternary); }
.role-badge { font-size: 10px; font-weight: 500; padding: 1px 7px; border-radius: var(--radius-pill); background: rgba(94,106,210,0.12); color: var(--accent-bright); text-transform: uppercase; }
.info-row { display: flex; gap: 12px; padding: 6px 0; border-bottom: 1px solid var(--border-subtle); }
.info-row:last-child { border-bottom: none; }
.info-label { font-size: 12px; color: var(--text-quaternary); min-width: 80px; }
.info-value { font-size: 13px; color: var(--text-secondary); }
.info-value.mono { font-family: var(--font-mono); }
.avatar-section { margin-bottom: 24px; }
.section-title { font-size: 14px; font-weight: 600; color: var(--text-secondary); margin-bottom: 12px; text-transform: capitalize; }
.legend { font-size: 12px; color: var(--text-quaternary); margin-bottom: 12px; }
.green { color: var(--success-bright); }
.orange { color: var(--warning); }
.red { color: var(--danger); }
.avatar-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; max-width: 540px; }
.avatar-cell { aspect-ratio: 1; border-radius: var(--radius-md); border: 2px dashed var(--border); display: flex; align-items: center; justify-content: center; cursor: pointer; transition: all 0.12s; position: relative; overflow: hidden; background: rgba(255,255,255,0.01); min-width: 160px; }
.avatar-cell:hover { border-color: var(--accent); border-style: solid; }
.upload-label { font-size: 11px; color: var(--text-quaternary); }
.cell-label { position: absolute; bottom: 2px; left: 4px; font-size: 9px; color: var(--text-quaternary); background: rgba(0,0,0,0.6); padding: 1px 5px; border-radius: 3px; }
.form-field { margin-bottom: 14px; display: flex; flex-direction: column; gap: 6px; }
.form-field label { font-size: 12px; font-weight: 500; color: var(--text-tertiary); }
.form-input { background: rgba(255,255,255,0.03); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 8px 12px; font-size: 14px; color: var(--text-primary); font-family: var(--font-sans); transition: border-color 0.12s; }
.form-input.mono { font-family: var(--font-mono); font-size: 13px; }
.form-input:focus { outline: none; border-color: var(--accent); }
.form-textarea { min-height: 80px; resize: vertical; }
.form-btn { padding: 8px 16px; background: var(--accent); color: #fff; border: none; border-radius: var(--radius-sm); font-size: 14px; font-weight: 500; cursor: pointer; font-family: var(--font-sans); transition: background 0.12s; }
.form-btn:hover { background: var(--accent-hover); }
.form-error { font-size: 13px; color: var(--danger); margin-bottom: 10px; }
.form-success { font-size: 13px; color: var(--success-bright); margin-bottom: 10px; }
.optional { font-size: 11px; font-weight: 400; color: var(--text-quaternary); }
</style>