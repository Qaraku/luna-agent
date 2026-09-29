package packages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// RetainedStateNamespaces只读返回catalog保留的包状态，不启用包、不执行探测。
func RetainedStateNamespaces(dir string) ([]string, error) {
	path := filepath.Join(dir, catalogName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCatalogBytes {
		return nil, fmt.Errorf("invalid package catalog for backup")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil || len(raw) > maxCatalogBytes {
		return nil, fmt.Errorf("package catalog exceeds backup bound")
	}
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid package catalog for backup")
	}
	if err := catalog.validate(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(catalog.Packages))
	for id := range catalog.Packages {
		names = append(names, "pkg-"+id)
	}
	sort.Strings(names)
	return names, nil
}
