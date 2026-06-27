<script>
let { page, onNavigate } = $props()

const items = [
  { key: 'runs', label: 'Runs', badge: true },
  { key: 'board', label: 'Board' },
  { key: 'tasks', label: 'Tasks' },
  { key: 'controller', label: 'Controller' },
  { key: 'settings', label: 'Settings' },
]
</script>

<aside class="sidebar">
  <div class="sidebar-header">
    <div class="logo">
      <div class="dot"></div>
      Control Room
    </div>
  </div>
  <nav class="nav">
    {#each items as item}
      <a
        class="nav-item"
        class:active={page === item.key}
        href="#"
        onclick={(e) => { e.preventDefault(); onNavigate(item.key) }}
      >
        <svg class="nav-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5">
          {#if item.key === 'runs'}
            <rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M4 6h4M4 9h6"/><circle cx="11.5" cy="6" r="1" fill="currentColor"/>
          {:else if item.key === 'board'}
            <rect x="1.5" y="2" width="4" height="12" rx="1"/><rect x="6.5" y="2" width="4" height="8" rx="1"/><rect x="11.5" y="2" width="3" height="5" rx="1"/>
          {:else if item.key === 'tasks'}
            <path d="M3 4h10M3 8h10M3 12h7"/><circle cx="13" cy="12" r="1.5" fill="currentColor"/>
          {:else if item.key === 'settings'}
            <circle cx="8" cy="8" r="2.5"/><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.2 3.2l1.4 1.4M11.4 11.4l1.4 1.4M3.2 12.8l1.4-1.4M11.4 4.6l1.4-1.4"/>
          {:else if item.key === 'controller'}
            <circle cx="8" cy="8" r="5.5"/><circle cx="8" cy="8" r="2" fill="currentColor"/>
          {/if}
        </svg>
        {item.label}
        {#if item.badge}
          <span class="badge">4</span>
        {/if}
      </a>
    {/each}
  </nav>
  <div class="footer">
    <div class="status-dot"></div>
    glm-5.2 · ollama-cloud
  </div>
</aside>

<style>
.sidebar { width: 220px; background: var(--bg-panel); border-right: 1px solid var(--border-subtle); display: flex; flex-direction: column; flex-shrink: 0; position: sticky; top: 0; height: 100vh; }
.sidebar-header { padding: 20px 16px 16px; border-bottom: 1px solid var(--border-subtle); }
.logo { display: flex; align-items: center; gap: 10px; font-size: 15px; font-weight: 600; letter-spacing: -0.24px; }
.dot { width: 10px; height: 10px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 8px rgba(113,112,255,0.4); }
.nav { flex: 1; padding: 12px 8px; display: flex; flex-direction: column; gap: 2px; }
.nav-item { display: flex; align-items: center; gap: 10px; padding: 7px 12px; border-radius: var(--radius-sm); font-size: 14px; font-weight: 500; color: var(--text-tertiary); cursor: pointer; transition: all 0.12s; text-decoration: none; letter-spacing: -0.13px; }
.nav-item:hover { background: rgba(255,255,255,0.04); color: var(--text-secondary); }
.nav-item.active { background: rgba(94,106,210,0.12); color: var(--accent-bright); }
.nav-icon { width: 16px; height: 16px; flex-shrink: 0; opacity: 0.7; }
.nav-item.active .nav-icon { opacity: 1; }
.badge { margin-left: auto; font-size: 11px; font-weight: 500; padding: 1px 7px; border-radius: var(--radius-pill); background: rgba(255,255,255,0.06); color: var(--text-tertiary); }
.nav-item.active .badge { background: rgba(94,106,210,0.2); color: var(--accent-bright); }
.footer { padding: 12px 16px; border-top: 1px solid var(--border-subtle); font-size: 12px; color: var(--text-quaternary); display: flex; align-items: center; gap: 8px; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--success); box-shadow: 0 0 6px rgba(39,166,68,0.4); }
@media (max-width: 768px) { .sidebar { display: none; } }
</style>