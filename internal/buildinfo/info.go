// Package buildinfo 提供不依赖配置与凭据的程序构建身份。
package buildinfo

import "runtime"

// Version和Commit由分发构建注入，不从tag或任务数量推导。
var Version = "dev"
var Commit = "unknown"

const DataSchema = 1

type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	DataSchema int    `json:"data_schema"`
}

func Current() Info { return Info{Version, Commit, runtime.GOOS, runtime.GOARCH, DataSchema} }
