// Package swarm — exec.LookPath 包装，便于测试 mock。
package swarm

import "os/exec"

// execLookPath 是 exec.LookPath 的直接包装，测试可替换。
var execLookPath = exec.LookPath
