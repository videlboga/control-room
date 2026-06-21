# Control Room

Control Room — это лёгкий orchestrator поверх Hermes Runtime. Он управляет проектами, командами, задачами и запусками агентов, изолирует их через git worktree и может распределять нагрузку по внешним worker-нодам.

## Архитектура

```
┌─────────────────────────────────────┐
│           Control Plane             │
│  ┌─────────┐  ┌─────────────────┐   │
│  │  CLI    │  │   HTTP API      │   │
│  │  (cr)   │  │   (planned)     │   │
│  └────┬────┘  └────────┬────────┘   │
│       │                │            │
│       └────────┬───────┘            │
│                │                    │
│       ┌────────▼────────┐           │
│       │    Scheduler      │          │
│       │  + Node Registry   │          │
│       └────────┬────────┘           │
│                │                     │
│       ┌────────▼────────┐           │
│       │   Local Store    │           │
│       │  (JSON/JSONL)    │           │
└───────┬─────────────────┬─────────────┘
        │                 │
   SSH/HTTP dispatch   Fallback
        │                 │
┌───────▼───────┐   ┌─────▼──────────┐
│  Worker Node  │   │  Control Plane │
│  (cr-worker)  │   │  local runner  │
│               │   │  (max 4 slots) │
│ - git + work  │   └────────────────┘
│ - hermes chat │
│ - log events  │
└───────────────┘
```

### Control Plane

- Хранит метаданные: проекты, команды, задачи, run'ы, зарегистрированные ноды.
- Принимает команды через CLI (`cr`).
- HTTP API — заготовка для будущего Web/TUI и внешних worker'ов.
- Scheduler выбирает свободную worker-ноду; при отсутствии нод запускает run локально с лимитом concurrency.
- Поддерживает JSONL-логи и работу с git worktree.

### Worker Node

- Отдельный бинарь `cr-worker`.
- Регистрируется на control plane.
- Шлёт heartbeat: статус, свободные слоты, память, load average.
- Выполняет run'ы, присланные control plane.
- Может быть развёрнут на любом хосте с Hermes Runtime и git.

## Быстрый старт

```bash
cd /root/Projects/control-room
go build -o /usr/local/bin/cr ./cmd/cr
cr project create --id demo --title Demo --repo /tmp/demo-repo
cr team create --id dev --name "Dev Team"
cr task create --project demo --team dev --title "Add feature"
cr run start --task <task-id>
```

## Concurrency limiter

По умолчанию на одном хосте одновременно выполняется не более 4 run'ов. Настройка через `workspace.yaml`:

```yaml
max_concurrent_runs: 4
```

Или CLI-флаг:

```bash
cr run start --task X --max-concurrent 2
```

## Worker-ноды (заготовка)

```bash
# зарегистрировать ноду
cr node add capalin --host 85.9.212.146 --user root --workspace /opt/cr-worker --max-concurrent 4

# запустить run на конкретной ноде
cr run start --task X --node capalin

# запустить run через scheduler
cr run start --task X
```

Полная реализация worker dispatch и HTTP API — в roadmap (см. TODO.md).

## Структура репозитория

```
cmd/
  cr/           # control-plane CLI
  cr-worker/    # worker daemon (заготовка)
internal/
  api/          # HTTP API contract
  cli/          # Cobra commands
  config/       # workspace config
  control/      # scheduler + node registry
  node/         # node model and remote client interface
  project/      # project CRUD
  run/          # run lifecycle, git worktree, concurrency slots
  store/        # filesystem store
  task/         # task CRUD
  team/         # team + agent profile management
```
