// Package maintenance 提供可信本地终端入口，不在HTTP服务暴露安装与数据恢复。
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
	"github.com/Qaraku/luna-agent/internal/release"
)

func Handle(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "version", "--version":
		if len(args) != 1 {
			return true, errors.New("version takes no arguments")
		}
		return true, json.NewEncoder(out).Encode(buildinfo.Current())
	case "release":
		return true, releaseCommand(args[1:], out)
	default:
		return false, nil
	}
}
func releaseCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("use: luna release inspect -dir PATH | install -archive PATH -dest NEW_DIRECTORY")
	}
	flags := flag.NewFlagSet("luna release "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "installed release directory")
	archive := flags.String("archive", "", "local tar.gz archive")
	dest := flags.String("dest", "", "new absolute installation directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected release argument")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var m release.Manifest
	var err error
	switch args[0] {
	case "inspect":
		if *dir == "" || *archive != "" || *dest != "" {
			return errors.New("inspect requires only -dir")
		}
		m, err = release.Verify(ctx, *dir)
	case "install":
		if *archive == "" || *dest == "" || *dir != "" {
			return errors.New("install requires -archive and -dest")
		}
		var f *os.File
		f, err = os.Open(*archive)
		if err != nil {
			return err
		}
		defer f.Close()
		m, err = release.Install(ctx, f, *dest)
	default:
		return fmt.Errorf("unknown release action %q", args[0])
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Version    string `json:"version"`
		Commit     string `json:"commit"`
		Identity   string `json:"identity"`
		DataSchema int    `json:"data_schema"`
		Restarted  bool   `json:"restarted"`
	}{m.Version, m.Commit, m.Identity(), m.DataSchema, false})
}
