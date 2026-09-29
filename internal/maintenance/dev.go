package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/Qaraku/luna-agent/internal/devworkspace"
)

type includedFiles []string

func (i *includedFiles) String() string { return "" }
func (i *includedFiles) Set(value string) error {
	if len(*i) >= 10000 {
		return errors.New("too many included files")
	}
	*i = append(*i, value)
	return nil
}
func devCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("use: luna dev create|status|export|inspect")
	}
	flags := flag.NewFlagSet("luna dev "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repo := flags.String("repo", "", "absolute local source repository")
	commit := flags.String("commit", "", "full local source commit")
	dest := flags.String("dest", "", "new absolute directory outside the source repository/workspace")
	workspace := flags.String("workspace", "", "development workspace containing workspace.json and source/")
	bundle := flags.String("bundle", "", "candidate bundle to inspect")
	var include includedFiles
	flags.Var(&include, "include", "new source file explicitly included in export; repeatable")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected development argument")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var result any
	var err error
	switch args[0] {
	case "create":
		if *repo == "" || *commit == "" || *dest == "" || *workspace != "" || *bundle != "" || len(include) > 0 {
			return errors.New("dev create requires only -repo, -commit and -dest")
		}
		created, createErr := devworkspace.Create(ctx, *repo, *commit, *dest)
		if createErr != nil {
			return createErr
		}
		result = struct {
			Commit          string `json:"source_commit"`
			Workspace       string `json:"workspace"`
			SourceDirectory string `json:"source_directory"`
			BaselineDigest  string `json:"baseline_digest"`
		}{created.SourceCommit, *dest, *dest + "/source", created.BaselineDigest}
	case "status":
		if *workspace == "" || *repo != "" || *commit != "" || *dest != "" || *bundle != "" || len(include) > 0 {
			return errors.New("dev status requires only -workspace")
		}
		result, err = devworkspace.Status(ctx, *workspace)
	case "export":
		if *workspace == "" || *dest == "" || *repo != "" || *commit != "" || *bundle != "" {
			return errors.New("dev export requires -workspace and -dest, with optional -include files")
		}
		result, err = devworkspace.Export(ctx, *workspace, *dest, include)
	case "inspect":
		if *bundle == "" || *repo != "" || *commit != "" || *dest != "" || *workspace != "" || len(include) > 0 {
			return errors.New("dev inspect requires only -bundle")
		}
		result, err = devworkspace.Inspect(ctx, *bundle)
	default:
		return errors.New("unknown development action; adoption remains an explicit user operation")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
