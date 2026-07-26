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
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Version:  %s\n", Version)
			fmt.Fprintf(out, "Date:     %s\n", BuildDate)
			fmt.Fprintf(out, "Go:       %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
