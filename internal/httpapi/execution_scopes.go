package httpapi

import (
	"fmt"
	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/store"
)

type executionScopes struct {
	ProjectDirs        []string `json:"project_dirs"`
	AutomaticWriteDirs []string `json:"automatic_write_dirs"`
	Network            string   `json:"network"`
	WriteProblem       string   `json:"write_problem,omitempty"`
}

func (s *Server) executionScopes(config *store.ConfigRecord) (executionScopes, error) {
	view := executionScopes{ProjectDirs: []string{}, AutomaticWriteDirs: []string{}, Network: "public_http_connect_proxy"}
	if config != nil && config.Workspace != "" {
		dirs, ok := s.workspaceDirs(config.Workspace)
		if !ok {
			return view, fmt.Errorf("the bound workspace no longer exists; select an available workspace")
		}
		view.ProjectDirs = append(view.ProjectDirs, dirs...)
	} else if s.fallbackRoot != "" {
		view.ProjectDirs = append(view.ProjectDirs, s.fallbackRoot)
	}
	if s.writeDirs == nil {
		return view, nil
	}
	automatic, err := s.readWriteDirs()
	if err != nil {
		view.WriteProblem = "automatic write scopes could not be read"
		return view, nil
	}
	seen := make(map[string]bool)
	for _, dir := range automatic {
		allowed, err := fileread.ResolveDirInRoots([]string{dir}, ".")
		if err != nil {
			continue
		}
		for _, root := range view.ProjectDirs {
			project, err := fileread.ResolveDirInRoots([]string{root}, ".")
			if err != nil {
				continue
			}
			intersection := ""
			if fileread.Within(project.Path, allowed.Path) {
				intersection = allowed.Path
			} else if fileread.Within(allowed.Path, project.Path) {
				intersection = project.Path
			}
			if intersection != "" && !seen[intersection] {
				seen[intersection] = true
				view.AutomaticWriteDirs = append(view.AutomaticWriteDirs, intersection)
			}
		}
	}
	return view, nil
}
