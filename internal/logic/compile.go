package logic

import (
	"fmt"
	"sort"
)

// gate 是编译后的门。
type gate struct {
	id     int
	kind   GateKind
	delay  int
	inputs []int // 驱动它的线的编号
	out    int   // 输出线编号
}

// circuit 是校验并拓扑排序后的可执行电路。
type circuit struct {
	gates     []gate
	inputOf   []int // 每条线的唯一驱动门；主输入为 -1
	wireNames []string
	inputs    []int // 主输入线编号
	initial   []bool
	topo      []int // 门的拓扑序
}

// compile 对请求做整份校验，拒绝任何非法输入。
// 校验项：主输入重名、门数上限、门延迟范围、门类型与元数、
// 输出线重复驱动（主输入与门输出同名也算重复驱动）、
// 输入引用未定义的线、组合环、观察点未定义、
// 翻转事件出现负时刻、未按时间严格排序（非法同时翻转）。
func compile(req *Request) (*circuit, error) {
	if len(req.Gates) > 40 {
		return nil, fmt.Errorf("电路最多包含 40 个门，当前为 %d 个", len(req.Gates))
	}
	if req.PulseThreshold <= 0 {
		return nil, fmt.Errorf("pulse_threshold 必须为正整数")
	}

	// 线编号：先主输入，后门输出。
	index := map[string]int{}
	names := make([]string, 0, len(req.Inputs)+len(req.Gates))
	inputOf := make([]int, 0, len(req.Inputs)+len(req.Gates))
	initial := make([]bool, 0, len(req.Inputs)+len(req.Gates))
	inputWires := make([]int, 0, len(req.Inputs))

	addWire := func(name string) (int, error) {
		if name == "" {
			return -1, fmt.Errorf("线名不能为空")
		}
		// 主输入与门输出共用同一个线命名空间；任何重名都意味着
		// 同一条线被两个源驱动（或主输入被重复声明），整份拒绝。
		if _, ok := index[name]; ok {
			return -1, fmt.Errorf("线 %q 被重复驱动", name)
		}
		id := len(names)
		index[name] = id
		names = append(names, name)
		inputOf = append(inputOf, -1)
		initial = append(initial, false)
		return id, nil
	}

	for i := range req.Inputs {
		in := &req.Inputs[i]
		id, err := addWire(in.Name)
		if err != nil {
			return nil, err
		}
		inputWires = append(inputWires, id)
		initial[id] = in.Initial

		// 翻转事件必须非负且严格递增：同一主输入不允许同时翻转两次。
		var prev int64
		first := true
		for _, ev := range in.Events {
			if ev.Time < 0 {
				return nil, fmt.Errorf("主输入 %q 存在负时刻事件 %d", in.Name, ev.Time)
			}
			if !first && ev.Time <= prev {
				return nil, fmt.Errorf("主输入 %q 的翻转事件未按时间严格排序（%d 处存在非法同时翻转）", in.Name, ev.Time)
			}
			prev = ev.Time
			first = false
		}
	}

	gates := make([]gate, len(req.Gates))
	for i := range req.Gates {
		g := &req.Gates[i]
		if g.Output == "" {
			return nil, fmt.Errorf("第 %d 个门缺少输出名", i+1)
		}
		out, err := addWire(g.Output)
		if err != nil {
			return nil, err
		}
		inputOf[out] = i
		if g.Delay < 1 || g.Delay > 8 {
			return nil, fmt.Errorf("门 %q 的延迟必须在 1~8 之间，当前为 %d", g.Output, g.Delay)
		}
		var arity int
		switch g.Kind {
		case GateAND, GateOR, GateXOR:
			arity = 2
		case GateNOT:
			arity = 1
		default:
			return nil, fmt.Errorf("门 %q 的类型 %q 非法（允许 AND/OR/XOR/NOT）", g.Output, g.Kind)
		}
		if len(g.Inputs) < arity {
			return nil, fmt.Errorf("门 %q 需要至少 %d 个输入，当前为 %d", g.Output, arity, len(g.Inputs))
		}

		cg := gate{id: i, kind: g.Kind, delay: g.Delay, out: out}
		seen := map[string]bool{}
		for _, inName := range g.Inputs {
			if seen[inName] {
				return nil, fmt.Errorf("门 %q 的输入 %q 重复连接", g.Output, inName)
			}
			seen[inName] = true
			inID, ok := index[inName]
			if !ok {
				// 引用了当前尚未定义的线。若它是后面门的输出，
				// 在门全部登记后再解析；这里先用占位 -1。
				inID = -1
			}
			cg.inputs = append(cg.inputs, inID)
		}
		gates[i] = cg
	}

	// 所有门输出线已登记，解析前向引用；不存在的名字在此拒绝。
	for i := range gates {
		for j, inID := range gates[i].inputs {
			if inID == -1 {
				name := req.Gates[i].Inputs[j]
				id, ok := index[name]
				if !ok {
					return nil, fmt.Errorf("门 %q 引用了未定义的线 %q", req.Gates[i].Output, name)
				}
				gates[i].inputs[j] = id
			}
		}
	}

	for _, name := range req.Observe {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("观察点 %q 未在电路中定义", name)
		}
	}

	// Kahn 拓扑排序，同时检测组合环。
	indeg := make([]int, len(gates))
	consumers := make([][]int, len(names)) // 线 -> 以它为输入的门
	for i := range gates {
		for _, in := range gates[i].inputs {
			d := inputOf[in]
			if d != -1 {
				indeg[i]++
				consumers[in] = append(consumers[in], i)
			}
		}
	}
	ready := make([]int, 0)
	for i, d := range indeg {
		if d == 0 {
			ready = append(ready, i)
		}
	}
	topo := make([]int, 0, len(gates))
	for len(ready) > 0 {
		g := ready[0]
		ready = ready[1:]
		topo = append(topo, g)
		for _, c := range consumers[gates[g].out] {
			indeg[c]--
			if indeg[c] == 0 {
				ready = append(ready, c)
			}
		}
	}
	if len(topo) != len(gates) {
		return nil, fmt.Errorf("电路存在组合环，无法求稳态")
	}

	c := &circuit{
		gates:     gates,
		inputOf:   inputOf,
		wireNames: names,
		inputs:    inputWires,
		initial:   initial,
		topo:      topo,
	}
	return c, nil
}

// consumersOf 返回以 wire 为输入的门列表（编译期缓存用）。
func (c *circuit) consumerIndex() [][]int {
	consumers := make([][]int, len(c.wireNames))
	for i := range c.gates {
		for _, in := range c.gates[i].inputs {
			consumers[in] = append(consumers[in], i)
		}
	}
	for i := range consumers {
		sort.Ints(consumers[i])
	}
	return consumers
}
