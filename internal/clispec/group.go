package clispec

import "github.com/spf13/cobra"

// newGroupCommand creates a parent command that prints help when run alone.
func newGroupCommand(g *Group) *cobra.Command {
	cmd := &cobra.Command{
		Use:   g.Use,
		Short: g.Short,
		Long:  g.Long,
		Annotations: map[string]string{
			GroupAnnotation: "true",
		},
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	return cmd
}
