# gatesim — 事件驱动门电路回放器

数字板输入最终稳定后，传播延迟不同的两条门路径仍可能在输出上形成
短脉冲（毛刺）。本项目用**事件驱动**方式回放门级电路，输出观察点的
完整跳变时间线与宽度小于阈值的脉冲；Compose 中的 `logic` 服务通过
JSON 接口接收电路。

## 语义

- 电路至多 40 个**单驱动**布尔门：`AND` / `OR` / `XOR`（2~3 输入）、
  `NOT`（1 输入），每门延迟为 1~8 个整数时刻。
- 连线必须无环（DAG）。主输入给初值与**按时间严格排序**的翻转事件。
- 先按拓扑序求**初始稳态**。
- 同一时刻的外部翻转与到期门事件**成批生效**；之后由本批受影响的门
  各算一次新输出，并按各自的传输延迟安排后续事件。
- **传输延迟（transport delay）语义**：只追加事件，绝不因为后来的变化
  取消此前已排队的待发事件——因此窄脉冲会如实保留。
- 到期值与当前值相同的事件为同值重复事件，直接忽略。
- 脉冲：观察点上连续三次跳变 `v→!v→v`，中间段宽度**严格小于**阈值即上报。
- 整份拒绝（400）：重复驱动（门输出重名 / 与主输入同名）、组合环、
  引用未定义线、非法门类型或元数、延迟越界、观察点未定义、
  事件负时刻/未排序/同一输入同刻多次翻转、门数超 40。

## 运行

```bash
docker compose up --build        # logic 服务 :8080
# 或本地
go run ./cmd/logicd
```

### 请求示例

```bash
curl -s -X POST localhost:8080/simulate -d '{
  "inputs":[{"name":"a","initial":false,"events":[{"time":1}]}],
  "gates":[
    {"output":"n","kind":"NOT","inputs":["a"],"delay":4},
    {"output":"q","kind":"XOR","inputs":["a","n"],"delay":2}
  ],
  "observe":["a","n","q"],
  "pulse_threshold":5
}'
```

重汇路径（延迟 2 与 4+2）在 `q` 上产生宽度 4 的低毛刺：

```json
{
  "initial": {"a": false, "n": true, "q": true},
  "timeline": [
    {"wire":"a","time":1,"to":true},
    {"wire":"q","time":3,"to":false},
    {"wire":"n","time":5,"to":false},
    {"wire":"q","time":7,"to":true}
  ],
  "pulses": [{"wire":"q","start":3,"width":4,"high":false}]
}
```

## 测试

`internal/logic` 同时包含一个**逐时刻参考模拟器**（不用事件队列，
按整数时刻推进并施加外部翻转、到期门事件、重算受影响门）。
测试用小电路与它逐时刻对拍，覆盖：

- 重汇路径毛刺（`TestReconvergingGlitch`）
- 同刻外部变化相互抵消（`TestSameTickOppositeCancellation`）
- 传输延迟不取消已排队事件，保留窄脉冲（`TestTransportDoesNotCancel`）
- 跨多个门的逐级延迟传播（`TestChainPropagation`）
- 同值重复事件忽略、全部非法输入拒绝（`TestSameValueIgnored`/`TestValidation`）
- 400 个随机 DAG 电路与参考模拟器对拍（`TestRandomAgainstReference`）

```bash
go test -race ./...
```

## 代码结构

```
internal/logic/
  types.go      请求/响应类型
  compile.go    整份校验 + 拓扑排序（Kahn，检环）
  engine.go     事件驱动引擎（事件堆、批量生效、脉冲检测）
  ref.go        逐时刻参考模拟器（测试对拍用）
  server.go     HTTP 接口 POST /simulate
cmd/logicd/     服务入口
```
