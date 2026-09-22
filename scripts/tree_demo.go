//go:build ignore

// Command tree_demo runs a cascade file exactly the way cmd/cascade does,
// except App.ExitWhenDone is left at its default (false), so the finished
// live tree holds itself open for browsing instead of returning to the
// shell immediately. cmd/cascade sets ExitWhenDone on purpose (see
// cmd/cascade/main.go); this is only for scripts/screenshots.sh, which
// needs the finished tree to still be on screen to photograph it.
//
//	go run scripts/tree_demo.go run
package main

import (
	"github.com/m-d-brown/cascade/cli"
	"github.com/m-d-brown/cascade/interpreter"
	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
	"github.com/spf13/pflag"
)

func main() {
	file := "cascade.yaml"
	cli.Main(cli.App{
		Name: "cascade",
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVarP(&file, "file", "f", file, "path to the cascade file")
		},
		Flow: func(ctx *work.Context) (string, error) {
			r, err := interpreter.Load(file)
			if err != nil {
				return "", work.Fatal(err)
			}
			return r.Run(ctx, world.Real())
		},
	})
}
