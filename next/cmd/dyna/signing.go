package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func signingCommand(get func() *socketClient) *cobra.Command {
	signing := &cobra.Command{Use: "signing"}
	signing.AddCommand(&cobra.Command{Use: "key", Short: "Print the public key that signs boxes' commits and tags", Long: "Print the engine's git signing key as one line. On GitHub, add it under Settings > SSH and GPG keys > New SSH key, with key type Signing key. The engine makes the key on first use; its private half never leaves the engine host. A room's boxes sign only while its signing is on (dyna room signing ROOM on).", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := call[contract.SigningKeyRequest, contract.SigningKeyResponse](cmd.Context(), get(), contract.MethodSigningKey, contract.SigningKeyRequest{})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), r.PublicKey)
		return err
	}})
	return signing
}

// runSigner is git's gpg.ssh.program in a box: git calls the dyna binary as `ssh-keygen -Y sign -n git -f KEYFILE [-U] FILE` and
// reads FILE.sig. The key file is ignored, since the engine holds the private key, so no ssh-keygen is needed. An error goes to
// stderr, where git shows it.
func runSigner(ctx context.Context, args []string, get func() *socketClient, stderr io.Writer) int {
	var file string
	var sign, git bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-Y":
			i++
			sign = i < len(args) && args[i] == "sign"
		case "-n":
			i++
			git = i < len(args) && args[i] == "git"
		case "-f":
			i++
		case "-U":
		default:
			file = args[i]
		}
	}
	if !sign || !git || file == "" {
		fmt.Fprintln(stderr, "dyna signs git commits and tags only (-Y sign -n git). To verify a signature, run git with -c gpg.ssh.program=ssh-keygen")
		return 1
	}
	payload, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	r, err := call[contract.GitSignRequest, contract.GitSignResponse](ctx, get(), contract.MethodGitSign, contract.GitSignRequest{Payload: payload})
	if err != nil {
		fmt.Fprintln(stderr, "signing refused:", err)
		return 1
	}
	if err := os.WriteFile(file+".sig", []byte(r.Signature), 0600); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
