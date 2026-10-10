// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

//go:build ignore

// zonenames_gen writes zonenames.txt: the time zones the preferences
// screen lists, one per line, from the zone data of the Go building it,
// which time/tzdata embeds, so that every name listed loads. Run it with
// go generate after a Go upgrade.
package main

import (
	"archive/zip"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// areas are the zone names' first parts that name places. The rest of the
// names are old aliases (US/Mountain, GB) or zones offset by hand, whose
// Etc/GMT+7 is seven hours behind, not ahead; UTC is the one kept.
var areas = []string{"Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic", "Australia", "Europe", "Indian", "Pacific"}

func main() {
	r, err := zip.OpenReader(filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close() //nolint:errcheck
	names := []string{"UTC"}
	for _, f := range r.File {
		area, _, ok := strings.Cut(f.Name, "/")
		if ok && !strings.HasSuffix(f.Name, "/") && slices.Contains(areas, area) {
			names = append(names, f.Name)
		}
	}
	slices.Sort(names)
	if err := os.WriteFile("zonenames.txt", []byte(strings.Join(names, "\n")+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
}
