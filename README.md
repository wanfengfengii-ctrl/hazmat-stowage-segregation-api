# 码头危险品预配载裁决 API

纯后端 HTTP 服务（Go 1.25 + Gin）：一次提交整份预配载清单，服务先逐字段校验，
再对**每个无序箱对**执行危险品隔离规则裁决，给出整票放行（`release`）或拦截结论；
被拦截的清单还可预演单箱移位（relocation-preview）或拆分为可执行的装载波次
（loading-waves）。服务无状态，所有结果均由当次请求实时计算，不存在假接口或固定响应。

## 裁决规则

按下列顺序检查每个无序箱对；同一箱对命中多条规则时，只报告**首个**原因。

| 顺序 | 规则码 | 箱对类别 | 拦截（冲突）条件 | 放行条件 |
|---|---|---|---|---|
| 1 | `CLASS_1_ISOLATION` | 1 与任何**其他**类别 | 一律冲突 | 两箱同为 1 类时不适用本规则 |
| 2 | `OXIDIZER_BAY_SEPARATION` | 5.1 与 3 或 4.1 | bay 差绝对值 < 2 | bay 差绝对值 ≥ 2 |
| 3 | `CORROSIVE_FLAMMABLE_GAS_SEPARATION` | 8 与 2.1 | deck 相同 **或** bay 差为 0 | deck 不同 **且** bay 差 ≥ 1 |
| — | 其余组合 | — | 兼容 | — |

> 说明：规则 1 原文为“类别 1 与任何**其他**类别一律冲突”，因此两箱同为 1 类
> 不属于“其他类别”，按兼容处理；这也是规则表中唯一需要解释的同类组合。

### 边界示例（可复核的唯一结论）

| 箱 A | 箱 B | 结论 | 依据 |
|---|---|---|---|
| 5.1, bay 5 | 3, bay 7 | 放行 | bay 差 = 2，不小于 2 |
| 5.1, bay 5 | 3, bay 6 | 拦截 | bay 差 = 1 < 2 |
| 5.1, bay 29 | 4.1, bay 30 | 拦截 | bay 差 = 1 < 2（bay 边界 30） |
| 8, U, bay 5 | 2.1, L, bay 6 | 放行 | deck 不同且 bay 差 = 1 |
| 8, U, bay 5 | 2.1, L, bay 5 | 拦截 | bay 差 = 0 |
| 8, U, bay 5 | 2.1, U, bay 9 | 拦截 | deck 相同 |
| 8, U, bay 1 | 2.1, L, bay 2 | 放行 | bay 边界 1/2，deck 不同且差 = 1 |
| 1, bay 1 | 8, bay 30 | 拦截 | 1 类与任何其他类别冲突 |
| 1, bay 1 | 1, bay 30 | 放行 | 同为 1 类，规则 1 不适用 |

## 快速开始

### Docker Compose（推荐）

```bash
docker compose up --build          # 仅启动 API，监听 http://localhost:8080
API_PORT=9090 docker compose up    # 宿主端口由 API_PORT 覆盖
```

`docker compose up` 只运行 API 一个常驻服务。

### 一次性验收服务 verify

```bash
docker compose --profile verify run --rm verify
```

`verify` 等待 API 健康后，用标准库测试套件直接请求运行中的接口
（`API_BASE_URL=http://api:8080`），跑完即退出，退出码即验收结果。

### 本地开发（需要 Go 1.25）

```bash
go test ./...        # 标准库测试：httptest 真实监听 + net/http 直接请求接口
go run ./cmd/api     # 启动服务，默认 :8080，PORT 环境变量可改
```

对已在运行的服务执行同一套验收测试：

```bash
API_BASE_URL=http://localhost:8080 go test ./... -count=1
```

## API 参考

### `POST /api/v1/pre-stowage/validate`

请求体为一份清单，`items` 至少 1 条；任一非法项将**整份拒绝**（HTTP 400）。

| 字段 | 要求 |
|---|---|
| `cargo_id` | 非空字符串，全单唯一（前后空白会被裁剪） |
| `hazard_class` | 仅 `"1"`、`"2.1"`、`"3"`、`"4.1"`、`"5.1"`、`"8"`；等价数字（如 `2.1`）同样接受 |
| `bay` | 1–30 的 JSON 整数（字符串、小数、越界均拒绝） |
| `deck` | 仅 `"U"`（上层）或 `"L"`（下层） |

放行请求示例：

```bash
curl -s -X POST http://localhost:8080/api/v1/pre-stowage/validate \
  -H 'Content-Type: application/json' \
  -d '{"items":[
        {"cargo_id":"C1","hazard_class":"3","bay":5,"deck":"U"},
        {"cargo_id":"C2","hazard_class":"4.1","bay":9,"deck":"L"},
        {"cargo_id":"C3","hazard_class":"2.1","bay":20,"deck":"U"}]}'
```

```json
{"release":true,"checked_pairs":3,"conflicts":[]}
```

拦截示例（1 类 + 5.1/3 相邻 + 8/2.1 同层）：

```bash
curl -s -X POST http://localhost:8080/api/v1/pre-stowage/validate \
  -H 'Content-Type: application/json' \
  -d '{"items":[
        {"cargo_id":"DELTA","hazard_class":"1","bay":4,"deck":"U"},
        {"cargo_id":"ALPHA","hazard_class":"5.1","bay":10,"deck":"U"},
        {"cargo_id":"CHARLIE","hazard_class":"3","bay":11,"deck":"L"},
        {"cargo_id":"BRAVO","hazard_class":"8","bay":20,"deck":"U"},
        {"cargo_id":"ECHO","hazard_class":"2.1","bay":22,"deck":"U"}]}'
```

```json
{
  "release": false,
  "checked_pairs": 10,
  "conflicts": [
    {"pair":["ALPHA","CHARLIE"],"rule":"OXIDIZER_BAY_SEPARATION","reason":"hazard class 5.1 and class 3 require bay separation >= 2 (ALPHA bay 10, CHARLIE bay 11)"},
    {"pair":["ALPHA","DELTA"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"},
    {"pair":["BRAVO","DELTA"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"},
    {"pair":["BRAVO","ECHO"],"rule":"CORROSIVE_FLAMMABLE_GAS_SEPARATION","reason":"hazard class 8 and class 2.1 require different decks and bay separation >= 1 (BRAVO deck U bay 20, ECHO deck U bay 22)"},
    {"pair":["CHARLIE","DELTA"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"},
    {"pair":["DELTA","ECHO"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"}
  ]
}
```

响应字段：

- `release`：无冲突为 `true`（放行），否则 `false`（拦截）。
- `checked_pairs`：实际检查的无序箱对数，即 `n*(n-1)/2`。
- `conflicts`：冲突箱对列表。箱号在对内升序，各对按箱号字典序排列；
  输入顺序不影响结果（换序提交得到逐字节相同的响应）。

### 校验错误（HTTP 400）

每个错误都指出字段路径，且一次返回全部错误：

```bash
curl -s -X POST http://localhost:8080/api/v1/pre-stowage/validate \
  -H 'Content-Type: application/json' \
  -d '{"items":[{"cargo_id":"","hazard_class":"7","bay":31,"deck":"X"},
                {"cargo_id":"B","hazard_class":"3","bay":5,"deck":"U"},
                {"cargo_id":"B","hazard_class":"3","bay":6,"deck":"L"}]}'
```

```json
{
  "error": "validation_failed",
  "details": [
    {"field":"items[0].cargo_id","message":"must not be empty"},
    {"field":"items[0].hazard_class","message":"must be one of \"1\", \"2.1\", \"3\", \"4.1\", \"5.1\", \"8\""},
    {"field":"items[0].bay","message":"must be an integer between 1 and 30"},
    {"field":"items[0].deck","message":"must be \"U\" or \"L\""},
    {"field":"items[2].cargo_id","message":"duplicates items[1].cargo_id (\"B\"); cargo_id must be unique in the manifest"}
  ]
}
```

请求体不是合法 JSON 时返回 `{"error":"invalid_json", ...}`，字段路径为 `(body)`。

### `POST /api/v1/pre-stowage/relocation-preview`

整票裁决被拦截后，预演“只移动某一个危险品箱”能否消除冲突。请求体在 `items`
（校验规则与 validate 完全一致）之外增加两个字段：

| 字段 | 要求 |
|---|---|
| `target_cargo_id` | 待移动箱子的 `cargo_id`，必须存在于清单中 |
| `candidate` | 候选位置对象，仅含 `bay`（1–30 整数）与 `deck`（`"U"`/`"L"`），校验规则与清单项一致；携带任何其他字段一律拒绝 |

服务先裁决原清单（`before`），再把目标箱替换到候选位置复算（`after`），两份裁决
与 validate 响应同格式；`resolved_conflicts` 与 `introduced_conflicts` 分别给出本次
移位消除与引入的冲突。冲突按（箱对， 规则）识别——同一箱对移动后仍违反同一规则时，
既不算消除也不算引入；两个列表均按箱对、再按规则码排序，结果确定。

```bash
curl -s -X POST http://localhost:8080/api/v1/pre-stowage/relocation-preview \
  -H 'Content-Type: application/json' \
  -d '{"items":[
        {"cargo_id":"ACID","hazard_class":"8","bay":5,"deck":"U"},
        {"cargo_id":"GAS","hazard_class":"2.1","bay":7,"deck":"U"},
        {"cargo_id":"NEUT","hazard_class":"3","bay":20,"deck":"L"}],
       "target_cargo_id":"GAS",
       "candidate":{"bay":7,"deck":"L"}}'
```

```json
{
  "before": {"release":false,"checked_pairs":3,"conflicts":[
    {"pair":["ACID","GAS"],"rule":"CORROSIVE_FLAMMABLE_GAS_SEPARATION","reason":"hazard class 8 and class 2.1 require different decks and bay separation >= 1 (ACID deck U bay 5, GAS deck U bay 7)"}]},
  "after": {"release":true,"checked_pairs":3,"conflicts":[]},
  "resolved_conflicts": [
    {"pair":["ACID","GAS"],"rule":"CORROSIVE_FLAMMABLE_GAS_SEPARATION","reason":"hazard class 8 and class 2.1 require different decks and bay separation >= 1 (ACID deck U bay 5, GAS deck U bay 7)"}],
  "introduced_conflicts": []
}
```

目标箱不存在（`target_cargo_id`）、候选位置非法（`candidate.bay`/`candidate.deck`）、
候选对象携带多余字段（`candidate.<字段名>`）或清单本身非法（`items[i].*`）时，
与 validate 一样整份拒绝：HTTP 400、准确的字段路径、一次返回全部错误，且响应中
不含任何部分裁决结果。

### `POST /api/v1/pre-stowage/loading-waves`

码头把同票内互相冲突的危险品箱拆成可执行的装载波次。请求体**仅含** `items`
（字段与校验规则和 validate 完全一致）；携带任何其他顶层字段一律整份拒绝
（HTTP 400，未知字段按字段名字典序排在清单错误之前）。

服务先按同一套规则完成裁决，再以**首个命中规则**构造无向冲突图，按下列判据
贪心着色分波：

1. 反复选择「相邻已占波次种类最多」的未分配箱；
2. 同值时按**总冲突度降序**、再按 `cargo_id` **升序**决胜；
3. 放入相邻箱**未占用的最小波次**（波次从 1 开始编号）。

保证：每个箱恰好出现一次，任一波次内部不得冲突；波次按编号、箱号按字典序输出；
无冲突清单合并为一个波次；输入换序得到逐字节相同的响应。

响应在 validate 的裁决字段（`release`、`checked_pairs`、`conflicts`）之外增加：

- `wave_count`：波次总数。
- `waves`：波次列表，每项含 `wave`（波次编号，从 1 开始）与 `cargo_ids`（该波次箱号，升序）。

```bash
curl -s -X POST http://localhost:8080/api/v1/pre-stowage/loading-waves \
  -H 'Content-Type: application/json' \
  -d '{"items":[
        {"cargo_id":"DELTA","hazard_class":"1","bay":4,"deck":"U"},
        {"cargo_id":"ALPHA","hazard_class":"5.1","bay":10,"deck":"U"},
        {"cargo_id":"CHARLIE","hazard_class":"3","bay":11,"deck":"L"},
        {"cargo_id":"BRAVO","hazard_class":"8","bay":20,"deck":"U"},
        {"cargo_id":"ECHO","hazard_class":"2.1","bay":22,"deck":"U"}]}'
```

```json
{
  "release": false,
  "checked_pairs": 10,
  "conflicts": [
    {"pair":["ALPHA","CHARLIE"],"rule":"OXIDIZER_BAY_SEPARATION","reason":"hazard class 5.1 and class 3 require bay separation >= 2 (ALPHA bay 10, CHARLIE bay 11)"},
    {"pair":["ALPHA","DELTA"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"},
    {"pair":["BRAVO","DELTA"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"},
    {"pair":["BRAVO","ECHO"],"rule":"CORROSIVE_FLAMMABLE_GAS_SEPARATION","reason":"hazard class 8 and class 2.1 require different decks and bay separation >= 1 (BRAVO deck U bay 20, ECHO deck U bay 22)"},
    {"pair":["CHARLIE","DELTA"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"},
    {"pair":["DELTA","ECHO"],"rule":"CLASS_1_ISOLATION","reason":"hazard class 1 conflicts with any other hazard class"}
  ],
  "wave_count": 3,
  "waves": [
    {"wave":1,"cargo_ids":["DELTA"]},
    {"wave":2,"cargo_ids":["ALPHA","BRAVO"]},
    {"wave":3,"cargo_ids":["CHARLIE","ECHO"]}
  ]
}
```

清单缺失、空数组、重复箱号、非法类别或位置、未知顶层字段时，与 validate 一样
整份拒绝：HTTP 400、一次返回全部错误（未知顶层字段按字段名字典序排在最前，随后
按清单位置及 `cargo_id`→`deck` 的业务顺序），且响应中不含任何部分波次结果。

### `GET /healthz`

健康检查，返回 `{"status":"ok"}`（Compose 健康检查与 verify 等待均使用它）。

## 环境变量

| 变量 | 作用域 | 说明 |
|---|---|---|
| `API_PORT` | docker compose | 宿主发布端口，默认 `8080` |
| `PORT` | API 进程 | 容器/进程内监听端口，默认 `8080` |
| `API_BASE_URL` | 测试 | 指向已运行的服务时，测试直接请求该地址；缺省则用 `httptest` 本地起服务 |

## 项目结构

```
cmd/api/main.go              进程入口：配置、启动、优雅退出
internal/stowage/            裁决领域逻辑（箱对规则、确定性排序、装载波次划分）及单元测试
internal/httpapi/            Gin 路由、逐字段校验（字段路径错误）及接口测试
Dockerfile                   多阶段：build / runtime(API) / verify(验收)
docker-compose.yml           仅常驻 API；verify 为一次性验收服务（profile）
```

## 测试

- `internal/stowage`：6×6 类别组合矩阵、bay 差边界（0/1/2）、deck×bay 四象限、
  1 类对所有类别、排序确定性与箱对数公式、移位前后冲突差集（resolved/introduced）；
  装载波次的确定性划分、换序不变、无冲突单波次及「每箱一次、波次内无冲突」不变量。
- `internal/httpapi`：标准库 `net/http` 直接请求接口——放行/拦截、边界箱位、
  输入换序响应逐字节一致、整份拒绝、字段路径、重复箱号、非法 JSON 等；
  移位预演覆盖消除冲突、引入冲突、无效目标/位置整份拒绝、清单换序结果不变，
  并交叉核对预演的 before/after 与 validate 裁决一致；装载波次覆盖多冲突清单的
  选点与分波判据、换序逐字节一致、无冲突单波次、非法清单与未知顶层字段整份拒绝，
  并交叉核对波次接口的裁决字段与 validate 一致。
