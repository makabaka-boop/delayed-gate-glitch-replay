package logic

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// buildResp 运行引擎并取参考模拟器结果，要求两者一致。
func compareWithReference(t *testing.T, req *Request) *Response {
	t.Helper()
	resp, err := Simulate(req)
	if err != nil {
		t.Fatalf("引擎回放失败: %v", err)
	}
	refTL, refInit, err := runReference(req)
	if err != nil {
		t.Fatalf("参考模拟器失败: %v", err)
	}
	if !reflect.DeepEqual(resp.Initial, refInit) {
		t.Fatalf("初始稳态不一致:\n引擎=%v\n参考=%v", resp.Initial, refInit)
	}
	if len(resp.Timeline) == 0 && len(refTL) == 0 {
		return resp
	}
	if !reflect.DeepEqual(resp.Timeline, refTL) {
		t.Fatalf("时间线不一致:\n引擎=%v\n参考=%v", resp.Timeline, refTL)
	}
	return resp
}

// TestReconvergingGlitch：同一次翻转经两条延迟不同的路径在 XOR
// 处重汇，产生一个宽度等于路径延迟差的毛刺。
func TestReconvergingGlitch(t *testing.T) {
	req := &Request{
		Inputs: []InputDecl{
			{Name: "a", Initial: false, Events: []ToggleEvt{{Time: 1}}},
		},
		Gates: []GateDecl{
			{Output: "n", Kind: GateNOT, Inputs: []string{"a"}, Delay: 4},
			{Output: "q", Kind: GateXOR, Inputs: []string{"a", "n"}, Delay: 2},
		},
		Observe:        []string{"a", "n", "q"},
		PulseThreshold: 5,
	}
	resp := compareWithReference(t, req)

	if !resp.Initial["q"] {
		t.Fatalf("初始 q 应为 1（0 xor 1），实际 %v", resp.Initial)
	}
	wantTL := []Transition{
		{Wire: "a", Time: 1, To: true},
		{Wire: "q", Time: 3, To: false}, // 短路径 1+2
		{Wire: "n", Time: 5, To: false},
		{Wire: "q", Time: 7, To: true}, // 长路径 1+4+2
	}
	if !reflect.DeepEqual(resp.Timeline, wantTL) {
		t.Fatalf("时间线不符:\n得到 %v\n想要 %v", resp.Timeline, wantTL)
	}
	wantPulses := []Pulse{{Wire: "q", Start: 3, Width: 4, High: false}}
	if !reflect.DeepEqual(resp.Pulses, wantPulses) {
		t.Fatalf("脉冲不符:\n得到 %v\n想要 %v", resp.Pulses, wantPulses)
	}
}

// TestSameTickOppositeCancellation：同一时刻两条输入同时翻转且
// 门输出保持不变，不应产生任何观察到的跳变。
func TestSameTickOppositeCancellation(t *testing.T) {
	req := &Request{
		Inputs: []InputDecl{
			{Name: "x", Initial: false, Events: []ToggleEvt{{Time: 1}}},
			{Name: "y", Initial: true, Events: []ToggleEvt{{Time: 1}}},
		},
		Gates: []GateDecl{
			{Output: "z", Kind: GateXOR, Inputs: []string{"x", "y"}, Delay: 3},
		},
		Observe:        []string{"x", "y", "z"},
		PulseThreshold: 3,
	}
	resp := compareWithReference(t, req)
	if len(resp.Timeline) != 2 { // 只有外部两条翻转可见
		t.Fatalf("同刻抵消后不应有门输出跳变，得到 %v", resp.Timeline)
	}
	for _, tr := range resp.Timeline {
		if tr.Wire == "z" {
			t.Fatalf("z 不应跳变: %v", resp.Timeline)
		}
	}
	if len(resp.Pulses) != 0 {
		t.Fatalf("不应有脉冲: %v", resp.Pulses)
	}
}

// TestTransportDoesNotCancel：后一次变化不得取消此前已排队的
// 待发事件。使能的 AND 输入连续 0->1->0（间隔小于门延迟），
// 输出仍应收到 1 个时刻的高脉冲。
func TestTransportDoesNotCancel(t *testing.T) {
	req := &Request{
		Inputs: []InputDecl{
			{Name: "in", Initial: false, Events: []ToggleEvt{{Time: 1}, {Time: 2}}},
			{Name: "en", Initial: true},
		},
		Gates: []GateDecl{
			{Output: "q", Kind: GateAND, Inputs: []string{"in", "en"}, Delay: 2},
		},
		Observe:        []string{"q"},
		PulseThreshold: 2,
	}
	resp := compareWithReference(t, req)
	wantTL := []Transition{
		{Wire: "q", Time: 3, To: true},
		{Wire: "q", Time: 4, To: false},
	}
	if !reflect.DeepEqual(resp.Timeline, wantTL) {
		t.Fatalf("时间线不符:\n得到 %v\n想要 %v", resp.Timeline, wantTL)
	}
	wantPulses := []Pulse{{Wire: "q", Start: 3, Width: 1, High: true}}
	if !reflect.DeepEqual(resp.Pulses, wantPulses) {
		t.Fatalf("应保留宽度 1 的高脉冲:\n得到 %v\n想要 %v", resp.Pulses, wantPulses)
	}

	// 严格小于：阈值为 1 时宽度 1 的脉冲不上报。
	req.PulseThreshold = 1
	resp2, err := Simulate(req)
	if err != nil {
		t.Fatalf("回放失败: %v", err)
	}
	if len(resp2.Pulses) != 0 {
		t.Fatalf("阈值 1 时宽度恰好 1 不应上报: %v", resp2.Pulses)
	}
}

// TestChainPropagation：翻转跨多个门逐级传播，每级延迟 1。
func TestChainPropagation(t *testing.T) {
	req := &Request{
		Inputs: []InputDecl{
			{Name: "a", Initial: false, Events: []ToggleEvt{{Time: 0}}},
		},
		Gates: []GateDecl{
			{Output: "n1", Kind: GateNOT, Inputs: []string{"a"}, Delay: 1},
			{Output: "n2", Kind: GateNOT, Inputs: []string{"n1"}, Delay: 1},
			{Output: "n3", Kind: GateNOT, Inputs: []string{"n2"}, Delay: 1},
		},
		Observe:        []string{"a", "n1", "n2", "n3"},
		PulseThreshold: 3,
	}
	resp := compareWithReference(t, req)
	wantTL := []Transition{
		{Wire: "a", Time: 0, To: true},
		{Wire: "n1", Time: 1, To: false},
		{Wire: "n2", Time: 2, To: true},
		{Wire: "n3", Time: 3, To: false},
	}
	if !reflect.DeepEqual(resp.Timeline, wantTL) {
		t.Fatalf("逐级传播时间线不符:\n得到 %v\n想要 %v", resp.Timeline, wantTL)
	}
}

// TestSameValueIgnored：输入翻转但门输出不变（如 OR 已有高输入），
// 到期事件为同值时必须忽略，时间线上无重复值。
func TestSameValueIgnored(t *testing.T) {
	req := &Request{
		Inputs: []InputDecl{
			{Name: "x", Initial: false, Events: []ToggleEvt{{Time: 1}, {Time: 2}}},
			{Name: "y", Initial: true},
		},
		Gates: []GateDecl{
			{Output: "q", Kind: GateOR, Inputs: []string{"x", "y"}, Delay: 2},
		},
		Observe:        []string{"q"},
		PulseThreshold: 3,
	}
	resp := compareWithReference(t, req)
	if len(resp.Timeline) != 0 {
		t.Fatalf("q 恒为 1，不应出现跳变: %v", resp.Timeline)
	}
	if !resp.Initial["q"] {
		t.Fatalf("初始 q 应为 true")
	}
}

// TestValidation：各种非法电路/事件必须整份拒绝。
func TestValidation(t *testing.T) {
	good := func() *Request {
		return &Request{
			Inputs:         []InputDecl{{Name: "a", Initial: false}},
			Gates:          []GateDecl{{Output: "q", Kind: GateNOT, Inputs: []string{"a"}, Delay: 1}},
			Observe:        []string{"q"},
			PulseThreshold: 2,
		}
	}

	t.Run("重复驱动-两门输出同名", func(t *testing.T) {
		req := good()
		req.Gates = append(req.Gates, GateDecl{Output: "q", Kind: GateNOT, Inputs: []string{"a"}, Delay: 1})
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝重复驱动")
		}
	})
	t.Run("主输入与门输出同名", func(t *testing.T) {
		req := good()
		req.Gates[0].Output = "a"
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝主输入与门输出同名")
		}
	})
	t.Run("组合环", func(t *testing.T) {
		req := good()
		req.Gates = []GateDecl{
			{Output: "b", Kind: GateNOT, Inputs: []string{"c"}, Delay: 1},
			{Output: "c", Kind: GateNOT, Inputs: []string{"b"}, Delay: 1},
		}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝组合环")
		}
	})
	t.Run("自环", func(t *testing.T) {
		req := good()
		req.Gates[0].Inputs = []string{"q"}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝自环")
		}
	})
	t.Run("引用未定义线", func(t *testing.T) {
		req := good()
		req.Gates[0].Inputs = []string{"zzz"}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝未定义引用")
		}
	})
	t.Run("观察点未定义", func(t *testing.T) {
		req := good()
		req.Observe = []string{"ghost"}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝未定义观察点")
		}
	})
	t.Run("延迟超范围", func(t *testing.T) {
		for _, d := range []int{0, -1, 9, 100} {
			req := good()
			req.Gates[0].Delay = d
			if _, err := Simulate(req); err == nil {
				t.Fatalf("应拒绝延迟 %d", d)
			}
		}
	})
	t.Run("非法门类型", func(t *testing.T) {
		req := good()
		req.Gates[0].Kind = "NAND"
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝非法门类型")
		}
	})
	t.Run("元数不足", func(t *testing.T) {
		req := good()
		req.Gates[0].Kind = GateAND
		req.Gates[0].Inputs = []string{"a"}
		if _, err := Simulate(req); err == nil {
			t.Fatal("AND 只有一个输入应拒绝")
		}
	})
	t.Run("同一输入同时翻转", func(t *testing.T) {
		req := good()
		req.Inputs[0].Events = []ToggleEvt{{Time: 5}, {Time: 5}}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝同一时刻的重复翻转")
		}
	})
	t.Run("事件未排序", func(t *testing.T) {
		req := good()
		req.Inputs[0].Events = []ToggleEvt{{Time: 5}, {Time: 3}}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝未按时间排序的事件")
		}
	})
	t.Run("负时刻", func(t *testing.T) {
		req := good()
		req.Inputs[0].Events = []ToggleEvt{{Time: -1}}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝负时刻事件")
		}
	})
	t.Run("门数超上限", func(t *testing.T) {
		req := good()
		req.Gates = req.Gates[:0]
		for i := 0; i < 41; i++ {
			in := "a"
			if i > 0 {
				in = gateName(i - 1)
			}
			req.Gates = append(req.Gates, GateDecl{
				Output: gateName(i), Kind: GateNOT, Inputs: []string{in}, Delay: 1,
			})
		}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝超过 40 个门")
		}
	})
	t.Run("重复输入连接", func(t *testing.T) {
		req := good()
		req.Gates[0].Kind = GateAND
		req.Gates[0].Inputs = []string{"a", "a"}
		if _, err := Simulate(req); err == nil {
			t.Fatal("应拒绝同一门重复连接同一根线")
		}
	})
}

func gateName(i int) string {
	return "g" + string(rune('a'+i%26)) + itoa(i/26)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	b := make([]byte, 0, 4)
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// genRandomCircuit 生成保证无环的随机小电路，用于与参考模拟器对拍。
func genRandomCircuit(rng *rand.Rand) *Request {
	nIn := 2 + rng.Intn(3)
	nGate := 3 + rng.Intn(12)
	wires := make([]string, 0, nIn+nGate)
	req := &Request{PulseThreshold: 1 + rng.Intn(6)}

	for i := 0; i < nIn; i++ {
		name := "in" + itoa(i)
		wires = append(wires, name)
		events := []ToggleEvt{}
		if rng.Intn(2) == 0 {
			t := int64(rng.Intn(3)) // 允许 0 时刻
			for k := 0; k < rng.Intn(6); k++ {
				events = append(events, ToggleEvt{Time: t})
				t += int64(1 + rng.Intn(5))
			}
		}
		req.Inputs = append(req.Inputs, InputDecl{
			Name: name, Initial: rng.Intn(2) == 0, Events: events,
		})
	}

	for i := 0; i < nGate; i++ {
		out := "w" + itoa(i)
		var kind GateKind
		var kinds = []GateKind{GateAND, GateOR, GateXOR, GateNOT}
		kind = kinds[rng.Intn(len(kinds))]
		nChoose := 2
		if kind == GateNOT {
			nChoose = 1
		} else if rng.Intn(3) == 0 {
			nChoose = 3 // 偶尔三输入
		}
		// 只从已存在的线里挑，保证无环。
		pool := rng.Perm(len(wires))
		if len(pool) < nChoose {
			nChoose = len(pool)
		}
		ins := make([]string, nChoose)
		for j := 0; j < nChoose; j++ {
			ins[j] = wires[pool[j]]
		}
		req.Gates = append(req.Gates, GateDecl{
			Output: out, Kind: kind, Inputs: ins, Delay: 1 + rng.Intn(8),
		})
		wires = append(wires, out)
	}

	req.Observe = append(req.Observe, wires...)
	return req
}

// TestRandomAgainstReference：大量随机 DAG 电路下事件引擎必须与
// 逐时刻参考模拟器产出完全相同的初始稳态与跳变时间线。
func TestRandomAgainstReference(t *testing.T) {
	for seed := int64(0); seed < 400; seed++ {
		rng := rand.New(rngForSeed(seed))
		req := genRandomCircuit(rng)
		compareWithReference(t, req)
	}
}

// rngForSeed 返回以 seed 播种的随机源（单独函数便于阅读）。
func rngForSeed(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// TestHTTPEndpoint：验证 logic 服务的 JSON 接口。
func TestHTTPEndpoint(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	req := &Request{
		Inputs: []InputDecl{{Name: "a", Initial: false, Events: []ToggleEvt{{Time: 1}}}},
		Gates: []GateDecl{
			{Output: "q", Kind: GateNOT, Inputs: []string{"a"}, Delay: 2},
		},
		Observe:        []string{"q"},
		PulseThreshold: 3,
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/simulate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d, 想要 200", resp.StatusCode)
	}
	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Initial["q"] {
		t.Fatalf("初始 q 应为 true")
	}
	if !reflect.DeepEqual(out.Timeline, []Transition{{Wire: "q", Time: 3, To: false}}) {
		t.Fatalf("时间线不符: %v", out.Timeline)
	}

	// 非法电路整份拒绝。
	bad := []byte(`{"inputs":[],"gates":[{"output":"q","kind":"NOT","inputs":["x"],"delay":1}],"observe":["q"],"pulse_threshold":2}`)
	resp2, err := http.Post(srv.URL+"/simulate", "application/json", bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 400 {
		t.Fatalf("非法电路状态码 = %d, 想要 400", resp2.StatusCode)
	}
}
