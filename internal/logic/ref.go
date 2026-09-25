package logic

import "sort"

// runReference 运行逐时刻参考模拟器：它不用事件队列，而是在每个
// 整数时刻 t 上（1）先统一施加该时刻的外部翻转，（2）再施加该
// 时刻到期的门输出，（3）找出受影响的门重算，并把新值写入
// t+delay 时刻的槽位。测试用它与事件驱动引擎对拍。返回观察点
// 跳变时间线（按与引擎相同的规则排序）与初始稳态。
func runReference(req *Request) ([]Transition, map[string]bool, error) {
	c, err := compile(req)
	if err != nil {
		return nil, nil, err
	}

	n := len(c.wireNames)
	consumers := c.consumerIndex()
	observed := map[int]bool{}
	for _, name := range req.Observe {
		observed[indexOf(c.wireNames, name)] = true
	}

	vals := make([]bool, n)
	copy(vals, c.initial)
	for _, gi := range c.topo {
		g := &c.gates[gi]
		vals[g.out] = g.eval(vals)
	}
	initial := map[string]bool{}
	for id, name := range c.wireNames {
		initial[name] = vals[id]
	}

	// external[t] = 在 t 时刻翻转的主输入。
	external := map[int64][]int{}
	var lastExt int64
	for _, inID := range c.inputs {
		decl := req.Inputs[inputIndex(req.Inputs, c.wireNames[inID])]
		for _, ev := range decl.Events {
			external[ev.Time] = append(external[ev.Time], inID)
			if ev.Time > lastExt {
				lastExt = ev.Time
			}
		}
	}
	horizon := lastExt + c.longestPathDelay()

	// slots[gate] 保存该门输出在某时刻到期的电平。同一 (门, 时刻)
	// 只会被写一次（同批变化只让门重算一次）。
	type slot struct {
		t int64
		v bool
	}
	pending := make([][]slot, len(c.gates))

	timeline := make([]Transition, 0)
	apply := func(wire int, v bool) bool {
		if vals[wire] == v {
			return false
		}
		vals[wire] = v
		return true
	}

	for t := int64(0); t <= horizon; t++ {
		changed := map[int]bool{}

		// 外部翻转先统一施加。
		for _, w := range external[t] {
			if apply(w, !vals[w]) {
				changed[w] = true
			}
		}

		// 到期门事件随后施加。
		for g := range c.gates {
			ps := pending[g]
			// 每个门每时刻至多一条，从头找到属于本时刻的条目。
			if len(ps) > 0 && ps[0].t == t {
				if apply(c.gates[g].out, ps[0].v) {
					changed[c.gates[g].out] = true
				}
				pending[g] = ps[1:]
			}
		}

		// 受影响门重算并写入未来槽位（无条件安排，同值不提前取消）。
		affected := map[int]bool{}
		for w := range changed {
			for _, g := range consumers[w] {
				affected[g] = true
			}
		}
		for g := range affected {
			gt := c.gates[g]
			nv := gt.eval(vals)
			pending[g] = append(pending[g], slot{t + int64(gt.delay), nv})
		}

		for w := range changed {
			if observed[w] {
				timeline = append(timeline, Transition{
					Wire: c.wireNames[w], Time: t, To: vals[w],
				})
			}
		}
	}

	sort.Slice(timeline, func(i, j int) bool {
		if timeline[i].Time != timeline[j].Time {
			return timeline[i].Time < timeline[j].Time
		}
		return timeline[i].Wire < timeline[j].Wire
	})
	return timeline, initial, nil
}
