# Calculator Server & Load Generator (Go)

Решение тестового задания: переписать с Python на Go HTTP-сервер калькулятора и генератор нагрузки, добавив ряд доработок.

## Условие задачи

Исходные программы на Python:

- **calculator_server** — HTTP-сервер, который на `POST /calc?num=X` вызывает функцию `add` из C-библиотеки и функцию `sub` из Rust-библиотеки, аккумулируя глобальные суммы `sum` и `sub`.
- **generator** — многопоточный генератор нагрузки на эндпоинт `/calc`.

### Требуемые доработки

1. Отвечать на `POST /calc?num=X` максимально быстро.
2. Добавить роут `GET /metrics` в формате Prometheus:
   - RPS HTTP-запросов за каждую секунду последней минуты (60 значений);
   - p95, p99 времени выполнения вызова функции на C;
   - p95, p99 времени выполнения вызова функции на Rust.
3. Переписать генератор нагрузки на Go.

## Структура проекта

```text
Go_test/
├── c_lib/
│   ├── calculator.c        # исходник C-библиотеки (функция add)
│   └── calculator.h
├── rust_lib/
│   ├── src/                # исходник Rust-библиотеки (функция sub)
│   └── Cargo.toml
├── build.sh                # скрипт сборки нативных библиотек
├── calculator_server.py    # исходная Python-версия сервера
├── generator.py            # исходная Python-версия генератора
├── server.go               # Go-версия сервера (с /metrics)
├── generator.go            # Go-версия генератора
└── go.mod
```

## Архитектурные решения

### Максимальная скорость `/calc`

- **Lock-free состояние.** Вместо мьютекса из Python-версии используется атомарный CAS-цикл (`atomic.CompareAndSwapInt64`): поток читает старое значение, вызывает нативную функцию и пытается записать результат; при конфликте повторяет. Это исключает блокировки и очереди из критического пути.
- **Замер латентности без накладных расходов.** Время вызова C/Rust-функции измеряется наносекундными метками (`time.Now().UnixNano()`) строго вокруг самого вызова, поэтому в p95/p99 не попадает время ожидания блокировок.
- **Lock-free кольцевые буферы метрик.** Латентности пишутся в pre-allocated массив на 100 000 элементов через атомарный индекс — без аллокаций и мьютексов в hot path.
- **Динамическая загрузка библиотек.** Как и в Python-версии (`ctypes`), библиотеки загружаются в рантайме через `dlopen`/`LoadLibrary` (кроссплатформенно, Windows и Linux), пути передаются флагами `--c-lib` и `--rust-lib`.

### Метрики `/metrics`

- **RPS за 60 секунд.** Фоновая горутина раз в секунду снимает атомарный счётчик запросов (`atomic.SwapUint64`) и складывает значение в кольцевой буфер на 60 ячеек. Отдаётся как 60 gauge-значений `rps{sec="N"}`.
- **p95 / p99.** По запросу `/metrics` содержимое кольцевого буфера латентностей копируется и сортируется, берутся точные перцентили. Сортировка происходит только в момент скрейпа, а не на каждом запросе.
- Формат вывода соответствует exposition format Prometheus (`# HELP`, `# TYPE`, метки).

### Генератор нагрузки

- Потоки Python заменены на горутины: меньше памяти, быстрее старт, большее число воркеров.
- `http.Client` с переиспользованием соединений (keep-alive) вместо создания запросов с нуля.
- Атомарные счётчики `ok`/`errors`, корректная остановка по SIGINT с итоговой статистикой.

## Сборка

### 1. Нативные библиотеки

Linux / WSL:

```bash
./build.sh
```

Вручную:

```bash
gcc -shared -fPIC -o libcalculator.so c_lib/calculator.c
cd rust_lib && cargo build --release   # libcalculator_rust.so в target/release
```

Windows (MinGW):

```powershell
gcc -shared -o libcalculator.dll c_lib/calculator.c
cd rust_lib; cargo build --release     # calculator_rust.dll в target/release
```

### 2. Go-бинарники

```bash
go build -o server server.go
go build -o generator generator.go
```

## Запуск

Сервер:

```bash
./server --c-lib ./libcalculator.so --rust-lib ./rust_lib/target/release/libcalculator_rust.so
```

Генератор (в отдельном терминале):

```bash
./generator -n 20 --interval 0.05 --url http://localhost:8080/calc
```

### Флаги сервера

| Флаг | По умолчанию | Описание |
|---|---|---|
| `--host` | `0.0.0.0` | адрес bind |
| `--port` | `8080` | порт |
| `--c-lib` | `libcalculator.so` | путь к C-библиотеке |
| `--rust-lib` | `libcalculator_rust.so` | путь к Rust-библиотеке |
| `--interval` | `5.0` | период печати sum/sub |

### Флаги генератора

| Флаг | По умолчанию | Описание |
|---|---|---|
| `--url` | `http://localhost:8080/calc` | эндпоинт |
| `-n` | `10` | число воркер-горутин |
| `--interval` | `0.1` | пауза между запросами воркера, сек (0 = без пауз) |
| `--timeout` | `5.0` | таймаут HTTP-запроса, сек |

## Метрики

```bash
curl http://localhost:8080/metrics
```

Пример вывода:

```text
# HELP rps Requests per second for each of the last 60 seconds
# TYPE rps gauge
rps{sec="0"} 1523
rps{sec="1"} 1487
...
# HELP c_func_latency_ns C function execution latency in nanoseconds
# TYPE c_func_latency_ns gauge
c_func_latency_p95_ns 245
c_func_latency_p99_ns 512
# HELP rust_func_latency_ns Rust function execution latency in nanoseconds
# TYPE rust_func_latency_ns gauge
rust_func_latency_p95_ns 198
rust_func_latency_p99_ns 387
```

## Корректное завершение

По SIGINT (`Ctrl+C`) сервер печатает финальные суммы:

```text
[final] sum=12345 sub=-6789
```

Генератор по SIGINT печатает итог запросов:

```text
Total requests: ok=15234 errors=0
```