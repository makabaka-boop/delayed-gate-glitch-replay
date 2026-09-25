package logic

import (
	"container/heap"
	"sort"
)

// eval 按门类型用当前线值计算输出。
func (g *gate) eval(vals []bool) bool {
	switch g.kind {
	case GateNOT:
		return !vals[g.inputs[0]]
	case GateAND:
		for _, in := range g.inputs {
			if !vals[in] {
				return false
			}
		}
		return true
	case GateOR:
		for _, in := range g.inputs {
			if vals[in] {
				return true
			}
		}
		return false
	case GateXOR:
		r := false
		for _, in := range g.inputs {
			r = r != vals[in]
		}
		return r
	}
	return false
}

// evtKind 区分事件来源。
type evtKind int

const (
	evtExternal evtKind = iota // 主输入的外部翻转
	evtGate                    // 门输出到期
)

// event 是事件队列中的一条待发事件。
type event struct {
	time  int64
	kind  evtKind
	gate  int  // evtGate 时的门编号
	wire  int  // 目标线（主输入或门输出）
	value bool // 到期时该线取的电平
}

type eventHeap []event

func (h eventHeap) Len() int            { return len(h) }
func (h eventHeap) Less(i, j int) bool  { return h[i].time < h[j].time }
func (h eventHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x interface{}) { *h = append(*h, x.(event)) }
func (h *eventHeap) Pop() interface{} {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
}

// 同一 (门, 到期时刻) 只允许安排一条事件：同一批变化中一条线
// 只可能把同一个门影响一次，但不同输入线在同批中都变化时，
// 用集合保证门只重算一次。
type scheduleKey struct {
	gate int
	time int64
}

// Simulate 校验电路并执行事件驱动回放，返回初始稳态、观察点
// 跳变时间线与宽度小于阈值的短脉冲。任何非法电路/事件都整份拒绝。
func Simulate(req *Request) (*Response, error) {
	c, err := compile(req)
	if err != nil {
		return nil, err
	}

	nWires := len(c.wireNames)
	consumers := c.consumerIndex()
	observed := make(map[int]bool, nWires)
	obsOrder := make([]int, 0, len(req.Observe))
	for _, name := range req.Observe {
		id := indexOf(c.wireNames, name)
		if !observed[id] {
			observed[id] = true
			obsOrder = append(obsOrder, id)
		}
	}
	sort.Ints(obsOrder)

	// 初始稳态：按拓扑序传播主输入初值。
	values := make([]bool, nWires)
	copy(values, c.initial)
	for _, gi := range c.topo {
		g := &c.gates[gi]
		values[g.out] = g.eval(values)
	}

	initial := make(map[string]bool, nWires)
	for id, name := range c.wireNames {
		initial[name] = values[id]
	}

	// 事件队列：预置全部外部翻转事件。
	h := &eventHeap{}
	heap.Init(h)
	var lastExt int64
	for _, inID := range c.inputs {
		// 主输入的事件列表在编译期已按时间排序。
		decl := req.Inputs[inputIndex(req.Inputs, c.wireNames[inID])]
		for _, ev := range decl.Events {
			if ev.Time > lastExt {
				lastExt = ev.Time
			}
			// 翻转后的电平在到期时按当前值取反，这里 value 不预填。
			heap.Push(h, event{time: ev.Time, kind: evtExternal, gate: -1, wire: inID})
		}
	}

	// 安全上界：最后一个外部事件之后再传过最长路径，队列必空。
	horizon := lastExt + c.longestPathDelay()

	timeline := make([]Transition, 0)
	scheduled := map[scheduleKey]bool{}

	for h.Len() > 0 {
		t := (*h)[0].time
		if t > horizon {
			break
		}

		// 1) 取出同一时刻的全部事件（外部翻转 + 到期门事件），成批处理。
		batch := make([]event, 0)
		for h.Len() > 0 && (*h)[0].time == t {
			batch = append(batch, heap.Pop(h).(event))
		}

		// 2) 逐条生效：同值重复事件直接忽略。
		changed := map[int]bool{}
		for _, e := range batch {
			val := e.value
			if e.kind == evtExternal {
				val = !values[e.wire] // 外部事件语义为翻转
			}
			if val == values[e.wire] {
				continue // 到期值与当前值相同：忽略同值重复事件
			}
			values[e.wire] = val
			changed[e.wire] = true
		}

		if len(changed) == 0 {
			continue
		}

		// 3) 受影响的门各算一次新输出，按各自传播延迟安排后续事件。
		affected := map[int]bool{}
		for w := range changed {
			for _, g := range consumers[w] {
				affected[g] = true
			}
		}
		for g := range affected {
			gt := c.gates[g]
			nv := gt.eval(values)
			due := t + int64(gt.delay)
			k := scheduleKey{g, due}
			if scheduled[k] {
				continue
			}
			scheduled[k] = true
			// 传输延迟语义：只追加新事件，绝不因为后来的变化而
			// 取消此前已排队的待发事件。
			heap.Push(h, event{time: due, kind: evtGate, gate: g, wire: gt.out, value: nv})
		}

		// 4) 记录本时刻观察点跳变。
		for w := range changed {
			if observed[w] {
				timeline = append(timeline, Transition{
					Wire: c.wireNames[w],
					Time: t,
					To:   values[w],
				})
			}
		}
	}

	// 时间线按 (时刻, 线名) 排序，保证输出稳定。
	sort.Slice(timeline, func(i, j int) bool {
		if timeline[i].Time != timeline[j].Time {
			return timeline[i].Time < timeline[j].Time
		}
		return timeline[i].Wire < timeline[j].Wire
	})

	resp := &Response{
		Initial:  initial,
		Timeline: timeline,
		Pulses:   detectPulses(c, initial, timeline, obsOrder, req.PulseThreshold),
	}
	return resp, nil
}

// longestPathDelay 以延迟为权求主输入到任意门输出的最长路径，
// 用于确定回放的安全截止时刻。
func (c *circuit) longestPathDelay() int64 {
	lp := make([]int, len(c.gates))
	for _, gi := range c.topo {
		g := &c.gates[gi]
		best := 0
		for _, in := range g.inputs {
			if p := c.inputOf[in]; p != -1 && lp[p] > best {
				best = lp[p]
			}
		}
		lp[gi] = best + g.delay
	}
	best := int64(0)
	for _, v := range lp {
		if int64(v) > best {
			best = int64(v)
		}
	}
	return best
}

// detectPulses 在观察点时间线上找出宽度严格小于阈值的脉冲。
// 时间线上的值严格交替（同值跳变已忽略），因此任意两次相邻
// 跳变之间的内段若宽度不足阈值，就是一个毛刺；最后一段无回归
// 跳变，视为未闭合，不上报。
func detectPulses(c *circuit, initial map[string]bool, tl []Transition, obsOrder []int, threshold int) []Pulse {
	byWire := make(map[int][]Transition, len(obsOrder))
	for _, tr := range tl {
		id := indexOf(c.wireNames, tr.Wire)
		byWire[id] = append(byWire[id], tr)
	}
	pulses := make([]Pulse, 0)
	for _, id := range obsOrder {
		trs := byWire[id]
		if len(trs) < 2 {
			continue
		}
		for i := 0; i+1 < len(trs); i++ {
			width := trs[i+1].Time - trs[i].Time
			if width < int64(threshold) {
				pulses = append(pulses, Pulse{
					Wire:  c.wireNames[id],
					Start: trs[i].Time,
					Width: width,
					High:  trs[i].To, // 脉冲段的电平即起始跳变后的电平
				})
			}
		}
	}
	sort.Slice(pulses, func(i, j int) bool {
		if pulses[i].Start != pulses[j].Start {
			return pulses[i].Start < pulses[j].Start
		}
		return pulses[i].Wire < pulses[j].Wire
	})
	return pulses
}

func indexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

func inputIndex(ins []InputDecl, name string) int {
	for i := range ins {
		if ins[i].Name == name {
			return i
		}
	}
	return -1
}
