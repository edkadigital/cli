package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/cli"
	"github.com/edkadigital/cli/internal/ui"
)

// version is set at build time: to the tag by release.yml, and to dev by the
// Makefile. `go install` sets none.
var version string

// tag is a version a release is tagged with, such as v1.2.3 or v1.3.0-rc.1.
var tag = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// buildVersion is the version of the binary: the one set at build time, else
// the tag `go install github.com/edkadigital/cli/cmd/edka@v1.2.3` records. Go
// records a pseudo-version for `@main` and for a build in a checkout, such as
// v1.2.4-0.20261003101500-0123456789ab, and that binary is dev.
func buildVersion(set string, info *debug.BuildInfo) string {
	if set != "" {
		return set
	}
	if info != nil && tag.MatchString(info.Main.Version) {
		return info.Main.Version
	}
	return "dev"
}

func main() { os.Exit(run()) }
func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	info, _ := debug.ReadBuildInfo()
	root := cli.New(buildVersion(version, info), os.Stdin, os.Stdout, os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		// A command started by `edka run` reports how it ended, interrupted or not.
		var child *cli.ExitError
		if errors.As(err, &child) {
			return child.Code
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return 130
		}
		code := 1
		message := ui.CleanText(err.Error())
		jsonMode, _ := root.PersistentFlags().GetBool("json")
		output, _ := root.PersistentFlags().GetString("output")
		if jsonMode || output == "json" || requestedJSON(os.Args[1:]) {
			v := map[string]any{"error": message, "exit_code": code}
			var apiError *api.Error
			if errors.As(err, &apiError) {
				v["status"] = apiError.Status
				v["request_id"] = apiError.RequestID
				// What the API's error body has beyond its message.
				for key, value := range map[string]string{"code": apiError.Code, "reason": apiError.Reason, "details": apiError.Details} {
					if value != "" {
						v[key] = value
					}
				}
				if len(apiError.Fields) > 0 {
					v["fields"] = apiError.Fields
				}
			}
			var settings *cli.FieldsError
			if errors.As(err, &settings) {
				v["fields"] = settings.Fields
			}
			_ = json.NewEncoder(os.Stderr).Encode(v)
		} else {
			fmt.Fprintln(os.Stderr, "Error: "+message)
		}
		return code
	}
	return 0
}

// Cobra can fail command discovery before parsing persistent flags. Preserve
// the requested error format for those failures too, stopping at child argv.
func requestedJSON(args []string) bool {
	jsonFlag, outputFlag := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch arg {
		case "--json", "--json=true":
			jsonFlag = true
		case "--json=false":
			jsonFlag = false
		case "--output":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				outputFlag = args[i] == "json"
			}
		default:
			if strings.HasPrefix(arg, "--output=") {
				outputFlag = strings.TrimPrefix(arg, "--output=") == "json"
			}
		}
	}
	return jsonFlag || outputFlag
}
