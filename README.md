# Hermes Workspace

Лёгкий project/team/run orchestrator поверх Hermes Runtime.

## Установка

```bash
cd /root/Projects/hermes-workspace
go build -o /usr/local/bin/hw ./cmd/hw
```

## Использование

```bash
hw project create --id myproject --title MyProject --repo /path/to/git/repo
hw team create --file team.json
hw task create --title "Сделать X" --project myproject --team myteam
hw run start --task task_xxx          # foreground + логи
hw run start --task task_xxx --detach # фон
hw run list
hw run logs run_xxx
```

## Агенты

Каждая роль в команде маппится на отдельный Hermes-профиль через поле `profile` в `team.json`:

```json
{
  "agents": {
    "coder": {"hermes_agent": "coder", "profile": "hw_agent_coder", "role": "worker"},
    "reviewer": {"hermes_agent": "reviewer", "profile": "hw_agent_reviewer", "role": "reviewer"}
  }
}
```

Профили создаются вручную:

```bash
hermes profile create hw_agent_coder --clone-from qwen8 --clone --no-alias --description "Coder agent"
```

## Требования

- Go 1.26+
- Hermes CLI под пользователем, от которого будут запускаться агенты (по умолчанию `cyberkitty`).
- Репозиторий проекта должен принадлежать тому же пользователю (чтобы git worktree не ругался на ownership).
