package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func deviceCommand(getClient func() client) *cobra.Command {
	device := &cobra.Command{Use: "device"}
	var pairScopes []string
	devicePair := &cobra.Command{Use: "pair", Args: cobra.NoArgs, Short: "Make a pairing code", Long: "Make a 10-character pairing code. It works once, lives 5 minutes, and dies after 5 wrong tries. A new code replaces the old one. Without --scope, the code grants every scope on a loopback route, and read and operate elsewhere.", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().DevicePair(cmd.Context(), contract.DevicePairRequest{Scopes: scopeList(pairScopes)})
		if err == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s expires %s\n", r.Code, r.ExpiresAt.Format(time.RFC3339))
		}
		return err
	}}
	devicePair.Flags().StringArrayVar(&pairScopes, "scope", nil, "Scope to grant (repeatable)")
	deviceList := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().DeviceList(cmd.Context(), contract.DeviceListRequest{})
		if err != nil {
			return err
		}
		for _, d := range r.Devices {
			scopes := make([]string, 0, len(d.Scopes))
			for _, scope := range d.Scopes {
				scopes = append(scopes, string(scope))
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, strings.Join(scopes, ","), d.LastSeenAt.Format(time.RFC3339), d.ExpiresAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	}}
	deviceRevoke := &cobra.Command{Use: "revoke ID", Args: cobra.ExactArgs(1), Short: "Revoke a paired device", Long: "Revoke a paired device. It fails on its next request, and an open stream or terminal ends within 15 seconds.", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceRevoke(cmd.Context(), contract.DeviceRevokeRequest{ID: args[0]})
		return err
	}}
	var setScopes []string
	deviceSetScopes := &cobra.Command{Use: "set-scopes ID", Args: cobra.ExactArgs(1), Short: "Replace the scopes of a paired device", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceSetScopes(cmd.Context(), contract.DeviceSetScopesRequest{ID: args[0], Scopes: scopeList(setScopes)})
		return err
	}}
	deviceSetScopes.Flags().StringArrayVar(&setScopes, "scope", nil, "Scope to grant (repeatable)")
	_ = deviceSetScopes.MarkFlagRequired("scope") // fails only for an undefined flag, and "scope" is defined above
	var setHostName string
	deviceSet := &cobra.Command{Use: "set ID --host HOST", Args: cobra.ExactArgs(1), Short: "Name the host a paired device is on", Long: "Name the host a paired device is on, as in --host omarchy, so the owner's inputs from that device say which machine they come from. --host '' clears it.", RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().DeviceSetHost(cmd.Context(), contract.DeviceSetHostRequest{ID: args[0], Host: contract.HostName(setHostName)})
		return err
	}}
	deviceSet.Flags().StringVar(&setHostName, "host", "", "Host the device is on (empty clears it)")
	_ = deviceSet.MarkFlagRequired("host") // fails only for an undefined flag
	device.AddCommand(devicePair, deviceList, deviceRevoke, deviceSetScopes, deviceSet)
	return device
}

// scopeList turns repeated --scope flags into typed scopes. The engine checks the values.
func scopeList(values []string) []contract.Scope {
	scopes := make([]contract.Scope, 0, len(values))
	for _, value := range values {
		scopes = append(scopes, contract.Scope(value))
	}
	return scopes
}
