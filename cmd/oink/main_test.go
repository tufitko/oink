package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// parseTG runs argv through a minimal cobra command and returns what
// terragruntArgs extracts from the positional args.
func parseTG(t *testing.T, argv []string) ([]string, error) {
	t.Helper()
	var got []string
	var gotErr error
	c := &cobra.Command{
		Use:           "run",
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			got, gotErr = terragruntArgs(cmd, args)
			return nil
		},
	}
	c.Flags().String("repo-root", ".", "")
	c.Flags().StringSlice("changed-dirs", nil, "")
	c.SetArgs(argv)
	if err := c.Execute(); err != nil {
		return nil, err
	}
	return got, gotErr
}

func TestTerragruntArgs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
		err  string
	}{
		{
			name: "flags then command and args after dash",
			argv: []string{"--repo-root", ".", "--", "plan", "-refresh=false"},
			want: []string{"plan", "-refresh=false"},
		},
		{
			name: "just command after dash",
			argv: []string{"--", "plan"},
			want: []string{"plan"},
		},
		{
			name: "changed-dirs flag is consumed, not part of tg args",
			argv: []string{"--changed-dirs", "a,b", "--", "apply", "-auto-approve"},
			want: []string{"apply", "-auto-approve"},
		},
		{
			name: "no dash is rejected",
			argv: []string{"plan"},
			err:  "missing terragrunt command",
		},
		{
			name: "empty is rejected",
			argv: []string{},
			err:  "missing terragrunt command",
		},
		{
			name: "positional before dash is rejected",
			argv: []string{"oops", "--", "plan"},
			err:  "unexpected arguments before --",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTG(t, tc.argv)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
