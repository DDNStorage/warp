/*
 * Warp (C) 2019-2020 MinIO, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package cli

import (
	"github.com/minio/cli"
	"github.com/minio/pkg/v2/console"
	"github.com/minio/warp/pkg/bench"
)

var listFlags = []cli.Flag{
	cli.IntFlag{
		Name:  "objects",
		Value: 10000,
		Usage: "Number of objects to upload. Rounded up to have equal concurrent objects.",
	},
	cli.IntFlag{
		Name:  "versions",
		Value: 1,
		Usage: "Number of versions to upload. If more than 1, versioned listing will be benchmarked",
	},
	cli.StringFlag{
		Name:  "obj.size",
		Value: "1KB",
		Usage: "Size of each generated object. Can be a number or 10KB/MB/GB. All sizes are base 2 binary.",
	},
	cli.BoolFlag{
		Name:  "metadata",
		Usage: "Enable extended MinIO ListObjects with metadata, by default this benchmarking uses ListObjectsV2 API.",
	},
	cli.IntFlag{
		Name:  "max-keys",
		Value: 1000,
		Usage: "Max keys to list per request",
	},
}

var listCmd = cli.Command{
	Name:   "list",
	Usage:  "benchmark list objects",
	Action: mainList,
	Before: setGlobalsFromContext,
	Flags:  combineFlags(globalFlags, ioFlags, listFlags, genFlags, benchFlags, analyzeFlags),
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS]
  -> see https://github.com/minio/warp#list

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}`,
}

// mainDelete is the entry point for get command.
func mainList(ctx *cli.Context) error {
	checkListSyntax(ctx)

	b := bench.List{
		Common:        getCommon(ctx, newGenSource(ctx, "obj.size")),
		Versions:      ctx.Int("versions"),
		Metadata:      ctx.Bool("metadata"),
		CreateObjects: ctx.Int("objects"),
		NoPrefix:      ctx.Bool("noprefix"),
		MaxKeys:       ctx.Int("max-keys"),
	}
	return runBench(ctx, &b)
}

func checkListSyntax(ctx *cli.Context) {
	if ctx.NArg() > 0 {
		console.Fatal("Command takes no arguments")
	}
	if ctx.Int("versions") < 1 {
		console.Fatal("At least one version must be tested")
	}
	if ctx.Int("objects") < 1 {
		console.Fatal("At least one object must be tested")
	}

	checkAnalyze(ctx)
	checkBenchmark(ctx)
}
