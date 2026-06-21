# Roadmap / TODO

## ✅ Готово (MVP)

- [x] Go CLI `cr` с командами project/team/task/run.
- [x] JSON/JSONL filesystem store.
- [x] JSONL-логи run-событий.
- [x] Git worktree для каждого run.
- [x] Реальные Hermes-агенты через `hermes --profile ... chat`.
- [x] Автосоздание Hermes-профилей для команд.
- [x] Передача документации проекта в промпт агентам.
- [x] Глобальный concurrency limiter через слоты (default 4).
- [x] Detached run'ы с double-fork.
- [x] 15-проектный параллельный тест + замеры памяти.

## 🏗️ В работе / ближайшие задачи

- [ ] Step timeout для зависших run'ов (сейчас 1 из 15 завис на review).
  - **Сначала наблюдение:** собрать статистику по реальным run'ам — сколько времени занимает каждый шаг, какие шаги зависают, что делает hermes в этот момент.
  - **Идея агента-наблюдателя:** лёгкий агент/скрипт, который читает `events.jsonl` долго выполняющихся run'ов и определяет, происходит ли в них что-то осмысленное (новые tool_call-события, прогресс в файловой системе, изменения git). Если активность есть — run считается живым и продолжает работать. Если активности нет N минут — помечать как stuck и предупреждать / убивать.
  - Параметры: step timeout, run timeout, heartbeat interval — решить после наблюдений.
- [ ] Worker node dispatch (SSH-based MVP):
  - [ ] node registry CRUD (`cr node add/list/remove`).
  - [ ] Scheduler выбирает ноду по available slots.
  - [ ] Control plane dispatch'ит run на worker по SSH.
  - [ ] Worker шлёт heartbeat и события обратно.
  - [ ] Control plane агрегирует логи с worker'ов.
- [ ] HTTP API control plane (`/api/v1/runs`, `/api/v1/nodes`).
- [ ] Worker daemon `cr-worker serve` с HTTP слушателем.
- [ ] Project-level rules / ADR.
- [ ] Связь project ↔ allowed teams.
- [ ] Remote git push веток агента после run.
- [ ] TUI / Web dashboard (отложено, но API для этого готовится).

## 🔧 Техдолг / улучшения

- [ ] Заменить SSH-dispatch на стабильный HTTP/gRPC transport.
- [ ] Queue с приоритетами и retry для failed dispatch.
- [ ] Per-team concurrency limit (не только глобальный).
- [ ] Persist task transitions as events.
- [ ] Graceful cancel — убивать hermes-процесс run'а, а не только менять статус.
- [ ] Stuck-run observer agent: анализ events.jsonl + filesystem + git diff для определения «живой» vs «зависший» run.
- [ ] Metrics endpoint (prometheus-style).
- [ ] Tests.

## 🚀 Масштабирование

- [ ] Worker pool с автоскейлингом.
- [ ] Centralized logging (push/pull логи с нод).
- [ ] Git remote / автопуш веток.
- [ ] Multi-tenant isolation (workspaces per user).
