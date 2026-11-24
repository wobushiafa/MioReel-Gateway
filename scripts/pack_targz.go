//go:build ignore

package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "usage: pack_targz <stageParentDir> <rootName> <outTarGz>\n")
		os.Exit(2)
	}
	stageParent := os.Args[1]
	rootName := os.Args[2]
	outFile := os.Args[3]

	f, err := os.Create(outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", outFile, err)
		os.Exit(1)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	baseDir := filepath.Join(stageParent, rootName)
	err = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stageParent, path)
		if err != nil {
			return err
		}
		tarPath := filepath.ToSlash(rel)

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = tarPath
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = "root"
		hdr.Gid = 0
		hdr.Gname = "root"

		if d.IsDir() {
			hdr.Name = strings.TrimSuffix(tarPath, "/") + "/"
			hdr.Mode = 0o755
			return tw.WriteHeader(hdr)
		}

		base := filepath.Base(path)
		if base == "media-gateway" || strings.HasSuffix(base, ".sh") {
			hdr.Mode = 0o755
		} else {
			hdr.Mode = 0o644
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, src)
		_ = src.Close()
		return copyErr
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk %s: %v\n", baseDir, err)
		os.Exit(1)
	}
}
