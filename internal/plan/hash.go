package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Hash 计算计划的稳定哈希（C-1）。
//
// apply 请求必须带上这个哈希，服务端重新生成计划后比对：不一致说明前端看到的
// 计划与实际要执行的不是同一份，必须拒绝并要求重跑 plan。
//
// 稳定性来源：
//   - 结构体字段顺序即声明顺序，json.Marshal 输出确定；
//   - map 由 encoding/json 按键排序输出；
//   - GeneratedAt 每次都不同，先归零再算，否则哈希永远对不上。
func Hash(p *Plan) string {
	if p == nil {
		return ""
	}
	cp := *p
	cp.GeneratedAt = ""
	data, err := json.Marshal(cp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
