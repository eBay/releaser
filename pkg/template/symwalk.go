package template

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Walk extends filepath.Walk to also follow symlinks. The scope should be within the boundary.
func Walk(path, boundary string, followSymlink bool, walkFn filepath.WalkFunc) error {
	boundary, err := filepath.Abs(boundary)
	if err != nil {
		return err
	}
	return walk(path, path, boundary, followSymlink, walkFn)
}

func walk(filename, linkDirname, boundary string, followSymlink bool, walkFn filepath.WalkFunc) error {
	if !followSymlink {
		return filepath.Walk(filename, walkFn)
	}
	followSymlinkWalkFunc := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(filename, path); err == nil {
			path = filepath.Join(linkDirname, rel)
		} else {
			return err
		}
		if isSymlink(info) {
			finalPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			// the boundary could be a symbolic link
			boundaryPath, err := filepath.EvalSymlinks(boundary)
			if err != nil {
				return err
			}
			within, err := withinBoundary(finalPath, boundaryPath)
			if err != nil {
				return err
			}
			if !within {
				return nil
			}
			info, err := os.Lstat(finalPath)
			if err != nil {
				return walkFn(path, info, err)
			}
			if info.IsDir() {
				return walk(finalPath, path, boundary, followSymlink, walkFn)
			}
		}

		return walkFn(path, info, err)
	}
	return filepath.Walk(filename, followSymlinkWalkFunc)
}

func isSymlink(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink == os.ModeSymlink
}

func withinBoundary(path, boundary string) (bool, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("failed to get absolute path: %v", err)
	}
	rel, err := filepath.Rel(boundary, path)
	if err != nil {
		return false, fmt.Errorf("failed to check boundary: %v", err)
	}
	if !strings.HasPrefix(rel, "..") {
		return true, nil
	}
	return false, nil
}
