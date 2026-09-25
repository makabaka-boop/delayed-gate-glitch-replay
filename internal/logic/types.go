// Package logic 实现事件驱动的门级电路回放器。
//
// 一个 circuit 描述至多 40 个单驱动布尔门（AND/OR/XOR/NOT），
// 每门带 1~8 个整数时刻的传播延迟，连线必须是无环 DAG。
// 主输入给定初值与按时间排序的翻转事件，引擎先求初始稳态，
// 再按传输延迟（transport delay）语义驱动事件队列：同一时刻
// 的外部翻转与到期门事件成批生效，受影响的门各自计算新输出
// 并按自己的延迟安排后续事件。
package logic

// GateKind 是支持的门类型。
type GateKind string

const (
	GateAND GateKind = "AND"
	GateOR  GateKind = "OR"
	GateXOR GateKind = "XOR"
	GateNOT GateKind = "NOT"
)

// InputDecl 是一条主输入声明：名字、初值和翻转事件列表。
type InputDecl struct {
	Name    string      `json:"name"`
	Initial bool        `json:"initial"`
	Events  []ToggleEvt `json:"events,omitempty"`
}

// ToggleEvt 是主输入在 Time 时刻的一次翻转（取反）。
type ToggleEvt struct {
	Time int64 `json:"time"`
}

// GateDecl 是一个门的声明。输出线以 Output 命名，Inputs 按
// 线名引用主输入或其它门的输出。
type GateDecl struct {
	Output string   `json:"output"`
	Kind   GateKind `json:"kind"`
	Inputs []string `json:"inputs"`
	Delay  int      `json:"delay"`
}

// Request 是 /simulate 接口的请求体。
type Request struct {
	// Inputs 为主输入声明；名字必须唯一。
	Inputs []InputDecl `json:"inputs"`
	// Gates 为按任意顺序给出的门声明（至多 40 个）。
	Gates []GateDecl `json:"gates"`
	// Observe 为需要输出跳变时间线的观察点（线名）。
	Observe []string `json:"observe"`
	// PulseThreshold 为短脉冲判定阈值，宽度严格小于该值的脉冲被上报。
	PulseThreshold int `json:"pulse_threshold"`
}

// Transition 是观察点时间线上的一次跳变。
type Transition struct {
	Wire string `json:"wire"`
	Time int64  `json:"time"`
	// To 为跳变后的电平。
	To bool `json:"to"`
}

// Pulse 描述观察点上一个宽度小于阈值的毛刺（glitch）。
// 若连续三段为 v->!v->v，中间段宽度小于阈值，则记录为一个脉冲：
// Start 为开始跳变时刻，Width 为脉冲宽度，High 表示脉冲为高电平。
type Pulse struct {
	Wire  string `json:"wire"`
	Start int64  `json:"start"`
	Width int64  `json:"width"`
	High  bool   `json:"high"`
}

// Response 是一次回放的结果。
type Response struct {
	// Initial 给出所有线（主输入与门输出）的初始稳态值。
	Initial map[string]bool `json:"initial"`
	// Timeline 为观察点的完整跳变时间线，按 (时间, 线) 排序。
	Timeline []Transition `json:"timeline"`
	// Pulses 为观察点上宽度严格小于阈值的脉冲。
	Pulses []Pulse `json:"pulses"`
}
