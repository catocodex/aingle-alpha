package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iotaledger/hive.go/logger"
	flag "github.com/spf13/pflag"

	"github.com/iotaledger/hive.go/node"

	"github.com/Ariwonto/aingle-alpha/pkg/config"
)

var (
	enabledPlugins  []string
	disabledPlugins []string

	version = flag.BoolP("version", "v", false, "Prints the AINGLE version")
	help    = flag.BoolP("help", "h", false, "Prints the AINGLE help")
)

func AddPluginStatus(name string, status int) {
	switch status {
	case node.Enabled:
		enabledPlugins = append(enabledPlugins, name)
	case node.Disabled:
		disabledPlugins = append(disabledPlugins, name)
	}
}

func getList(a []string) string {
	sort.Strings(a)
	return strings.Join(a, " ")
}

func ParseConfig() {
	if err := config.FetchConfig(); err != nil {
		panic(err)
	}
	parseParameters()

	if err := logger.InitGlobalLogger(config.NodeConfig); err != nil {
		panic(err)
	}
}

func PrintConfig() {
	config.PrintConfig([]string{config.CfgWebAPIBasicAuthPasswordHash, config.CfgWebAPIBasicAuthPasswordSalt, config.CfgDashboardBasicAuthPasswordHash, config.CfgDashboardBasicAuthPasswordSalt})
}

// HideConfigFlags hides all non essential flags from the help/usage text.
func HideConfigFlags() {
	config.HideConfigFlags()
}

// PrintVersion prints out the AINGLE version
func PrintVersion() {
	flag.Parse()
	if *version {
		fmt.Println(AppName + " " + AppVersion)
		os.Exit(0)
	}
	if *help {
		flag.Usage()
		os.Exit(0)
	}
}

func printUsage() {
	fmt.Fprintf(
		os.Stderr,
		"\n"+
			"AINGLE\n\n"+
			"  A lightweight modular AINGLE node.\n\n"+
			"Usage:\n\n"+
			"  %s [OPTIONS]\n\n"+
			"Options:\n",
		filepath.Base(os.Args[0]),
	)
	flag.PrintDefaults()

	fmt.Fprintf(os.Stderr, "\nThe following plugins are enabled: %s\n", getList(config.NodeConfig.GetStringSlice(node.CFG_ENABLE_PLUGINS)))
	fmt.Fprintf(os.Stderr, "\nThe following plugins are disabled: %s\n", getList(config.NodeConfig.GetStringSlice(node.CFG_DISABLE_PLUGINS)))
}
