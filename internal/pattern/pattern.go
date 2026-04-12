package pattern

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Pattern struct {
	Name   string
	System string
}

func LoadPattern(patternsPath, name string) (*Pattern, error) {
	// Look in local .foo/patterns first, then config path
	paths := []string{
		filepath.Join(".foo", "patterns", name, "system.md"),
		filepath.Join(patternsPath, name, "system.md"),
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil {
			return &Pattern{
				Name:   name,
				System: string(content),
			}, nil
		}
	}

	return nil, fmt.Errorf("pattern %q not found", name)
}

func Create(patternsPath, name, system string) error {
	path := filepath.Join(patternsPath, name, "system.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(system), 0644)
}

func Import(patternsPath, srcPath, name string) error {
	if name == "" {
		name = filepath.Base(filepath.Dir(srcPath))
		if name == "." || name == "/" || name == "patterns" {
			name = strings.TrimSuffix(filepath.Base(srcPath), ".md")
		}
	}

	destDir := filepath.Join(patternsPath, name)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dest, err := os.Create(filepath.Join(destDir, "system.md"))
	if err != nil {
		return err
	}
	defer dest.Close()

	_, err = io.Copy(dest, src)
	return err
}

func Remove(patternsPath, name string) error {
	path := filepath.Join(patternsPath, name)
	return os.RemoveAll(path)
}

func List(patternsPath string) ([]string, error) {
	patternsDirs := []string{
		filepath.Join(".foo", "patterns"),
		patternsPath,
	}

	seen := make(map[string]bool)
	var out []string
	for _, dir := range patternsDirs {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() && !seen[f.Name()] {
				out = append(out, f.Name())
				seen[f.Name()] = true
			}
		}
	}
	return out, nil
}
