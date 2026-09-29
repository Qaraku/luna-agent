package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/datalifecycle"
	"github.com/Qaraku/luna-agent/internal/layout"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/memory"
	packageplugins "github.com/Qaraku/luna-agent/internal/plugins/packages"
	"github.com/Qaraku/luna-agent/internal/plugins/presets"
	skillsplugin "github.com/Qaraku/luna-agent/internal/plugins/skills"
	"github.com/Qaraku/luna-agent/internal/privatebackup"
	"github.com/Qaraku/luna-agent/internal/provider"
	"github.com/Qaraku/luna-agent/internal/settings"
)

func makeDataPlan(paths layout.Paths, sessions, state, configPath string, skillDirs []string) (privatebackup.Plan, error) {
	p := privatebackup.Plan{Format: 1, ConfigDir: paths.Config, DataDir: paths.Data, StateDir: state, SessionsDir: sessions, ConfigFile: configPath, SkillDirs: append([]string{}, skillDirs...)}
	for _, value := range []*string{&p.ConfigDir, &p.DataDir, &p.StateDir, &p.SessionsDir, &p.ConfigFile} {
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return p, err
		}
		*value = absolute
	}
	for i, path := range p.SkillDirs {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return p, err
		}
		p.SkillDirs[i] = absolute
	}
	return p, p.Validate()
}

// 组合根只选择已知数据资源；旧state根是源码仓库时也不会把整仓库复制进去。
func backupSources(p privatebackup.Plan) ([]privatebackup.Source, []string, error) {
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	sources := []privatebackup.Source{
		{ID: "configuration", Path: p.ConfigFile, Target: config.FileName},
		{ID: "providers", Path: providerFileFor(p.ConfigDir), Target: provider.FileName},
		{ID: "settings", Path: settingsFileFor(p.ConfigDir), Target: settings.FileName},
		{ID: "workspaces", Path: workspaceFile(p.DataDir), Target: filepath.Base(workspaceFile(p.DataDir))},
		{ID: "sessions", Path: p.SessionsDir, Target: "sessions", Directory: true},
		{ID: "skills", Path: filepath.Join(p.DataDir, "skills"), Target: "skills", Directory: true},
	}
	memoryDir, err := plugin.StateDirFor(memory.Descriptor(), p.StateDir)
	if err != nil {
		return nil, nil, err
	}
	sources = append(sources, privatebackup.Source{ID: "memory", Path: filepath.Join(memoryDir, memory.StateFileName), Target: filepath.ToSlash(filepath.Join(filepath.Base(memoryDir), memory.StateFileName))})
	for _, descriptor := range []plugin.Descriptor{skillsplugin.ManagedDescriptor(), presets.Descriptor(), packageplugins.ManagerDescriptor()} {
		dir, err := plugin.StateDirFor(descriptor, p.StateDir)
		if err != nil {
			return nil, nil, err
		}
		sources = append(sources, privatebackup.Source{ID: "ability-" + descriptor.ID, Path: dir, Target: filepath.Base(dir), Directory: true})
	}
	packageDir, err := plugin.StateDirFor(packageplugins.ManagerDescriptor(), p.StateDir)
	if err != nil {
		return nil, nil, err
	}
	names, err := packageplugins.RetainedStateNamespaces(packageDir)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range names {
		sources = append(sources, privatebackup.Source{ID: name, Path: filepath.Join(p.StateDir, name), Target: name, Directory: true})
	}
	extras := []string{}
	for i, dir := range p.SkillDirs {
		target := fmt.Sprintf("imported-skills/%d", i+1)
		sources = append(sources, privatebackup.Source{ID: fmt.Sprintf("extra-skills-%d", i+1), Path: dir, Target: target, Directory: true})
		extras = append(extras, target)
	}
	return sources, extras, nil
}
func readDataPlan(path string) (privatebackup.Plan, error) {
	var empty privatebackup.Plan
	before, statErr := os.Lstat(path)
	if statErr != nil {
		return empty, statErr
	}
	if !before.Mode().IsRegular() {
		return empty, errors.New("data plan must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return empty, errors.New("data plan must be a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (32<<10)+1))
	if err != nil {
		return empty, err
	}
	return privatebackup.DecodePlan(raw)
}
func dataCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("use: luna data plan|backup|inspect|restore")
	}
	flags := flag.NewFlagSet("luna data "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	planFile := flags.String("plan", "", "exported data locations plan")
	rootFlag := flags.String("root", "", "runtime resource root")
	sessionsFlag := flags.String("sessions-dir", "", "same sessions override as the application")
	stateFlag := flags.String("state-dir", "", "same state override as the application")
	configFlag := flags.String("config-file", "", "same config override as the application")
	var skills repeatedPath
	flags.Var(&skills, "skills-dir", "same extra skill root; repeatable")
	output := flags.String("out", "", "new absolute private output file")
	archive := flags.String("archive", "", "private local archive")
	dest := flags.String("dest", "", "new absolute restore directory")
	offline := flags.Bool("offline", false, "confirm all old Luna instances and external writers are stopped")
	includePrivate := flags.Bool("include-private", false, "confirm archive/restoration includes credentials and private records")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected data argument")
	}
	switch args[0] {
	case "plan", "backup", "inspect", "restore":
	default:
		return errors.New("unknown data operation")
	}
	locationFlags := *rootFlag != "" || *sessionsFlag != "" || *stateFlag != "" || *configFlag != "" || len(skills) > 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var m privatebackup.Manifest
	if args[0] == "inspect" || args[0] == "restore" {
		if *archive == "" || *output != "" || *planFile != "" || locationFlags || *offline {
			return errors.New("inspect/restore accepts only archive and restore destination/confirmation")
		}
		var err error
		if args[0] == "inspect" {
			if *dest != "" || *includePrivate {
				return errors.New("inspect requires only -archive")
			}
			m, err = privatebackup.Inspect(ctx, *archive)
		} else {
			if !*includePrivate || *dest == "" {
				return errors.New("restore requires -archive, -dest and -include-private")
			}
			m, err = privatebackup.Restore(ctx, *archive, *dest)
		}
		if err != nil {
			return err
		}
		return writeBackupSummary(out, m, *dest)
	}
	if *archive != "" || *dest != "" {
		return errors.New("plan/backup does not accept archive or restore destination")
	}
	if args[0] == "backup" && (!*offline || !*includePrivate || *output == "") {
		return errors.New("backup requires -out, -offline and -include-private; the archive may contain credentials")
	}
	if args[0] == "plan" && (*offline || *includePrivate) {
		return errors.New("plan does not read private data; backup confirmations are not needed")
	}
	var plan privatebackup.Plan
	var err error
	if *planFile != "" {
		if locationFlags {
			return errors.New("a data plan cannot be combined with location overrides")
		}
		plan, err = readDataPlan(*planFile)
	} else {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil && strings.TrimSpace(os.Getenv(layout.HomeEnv)) == "" {
			return homeErr
		}
		paths, resolveErr := layout.Resolve(os.Getenv, home)
		if resolveErr != nil {
			return resolveErr
		}
		executable, exeErr := os.Executable()
		if exeErr != nil {
			return exeErr
		}
		root, rootErr := resolveRoot(*rootFlag, executable, workingDirOrEmpty())
		if rootErr != nil {
			return rootErr
		}
		var sessions, state string
		if strings.TrimSpace(os.Getenv(layout.HomeEnv)) != "" {
			sessions, state = pinnedLocations(paths)
		} else {
			sessions, state, _ = localData(paths.Data, root)
		}
		sessions, _ = explicitOr(*sessionsFlag, sessions, "sessions")
		state, _ = explicitOr(*stateFlag, state, "state")
		plan, err = makeDataPlan(paths, sessions, state, configFileFor(*configFlag, paths.Config), skills)
	}
	if err != nil {
		return err
	}
	if args[0] == "plan" {
		if *output == "" {
			return json.NewEncoder(out).Encode(plan)
		}
		if !filepath.IsAbs(*output) {
			return errors.New("plan output must be an absolute new path")
		}
		raw, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(raw)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(*output)
			return writeErr
		}
		return closeErr
	}
	guard, err := datalifecycle.Acquire(plan.LockPaths())
	if err != nil {
		return err
	}
	defer guard.Close()
	sources, extra, err := backupSources(plan)
	if err != nil {
		return err
	}
	m, err = privatebackup.Backup(ctx, sources, *output, extra)
	if err != nil {
		return err
	}
	return writeBackupSummary(out, m, "")
}
func writeBackupSummary(out io.Writer, m privatebackup.Manifest, dest string) error {
	files, missing := 0, 0
	var bytes int64
	for _, e := range m.Entries {
		if !e.Directory {
			files++
			bytes += e.Bytes
		}
	}
	for _, s := range m.Sources {
		if !s.Present {
			missing++
		}
	}
	return json.NewEncoder(out).Encode(struct {
		Build       any      `json:"build"`
		DataSchema  int      `json:"data_schema"`
		Files       int      `json:"files"`
		Bytes       int64    `json:"bytes"`
		Missing     int      `json:"absent_sources"`
		Home        string   `json:"restored_home,omitempty"`
		ExtraSkills []string `json:"extra_skills,omitempty"`
		Restarted   bool     `json:"restarted"`
	}{m.Build, m.DataSchema, files, bytes, missing, dest, m.ExtraSkills, false})
}
