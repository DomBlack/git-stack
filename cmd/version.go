package cmd

import (
	"encoding/json/v2"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/DomBlack/git-stack/pkg/version"
)

func newVersionCmd(_ *cli) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the git-stack version",
		Long: `Print the version of this binary.

Release builds print the tag (v1.2.3). A binary built from a checkout prints
"dev" with the commit it was built from, the commit date and whether the tree
had uncommitted changes.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNothing,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Current()
			out := cmd.OutOrStdout()
			if asJSON {
				b, err := json.Marshal(info)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(out, string(b))
				return err
			}
			_, err := fmt.Fprintf(out, "git-stack %s\n%s %s/%s\n", info, info.GoVersion, info.OS, info.Arch)
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the version details as JSON")
	return cmd
}
