package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var (
	Version   = "Unknown"
	BuildDate = "n/a"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Version:  %s\n", Version)
			fmt.Printf("Date:     %s\n", BuildDate)
			fmt.Printf("Go:       %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
