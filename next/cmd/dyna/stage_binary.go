package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/spf13/cobra"
)

// This deploy-only process owns its artifact and SSH masters independently of the engine service.
func stageBinaryCommand() *cobra.Command {
	var data, hostData, runtimeDir, report string
	cmd := &cobra.Command{Use: "stage-host-binaries", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		for _, path := range []string{data, hostData, runtimeDir, report} {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("staging paths must be absolute")
			}
		}
		hosts, err := store.DeployHosts(cmd.Context(), data)
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(executable)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(raw)
		sum := hex.EncodeToString(hash[:])
		rawBytes := len(raw)
		raw = nil
		cache := podman.NewBinaryCache(func() ([]byte, error) { return os.ReadFile(executable) })
		if err := os.MkdirAll(report, 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(runtimeDir, 0700); err != nil {
			return err
		}
		pending := make(map[contract.HostName]bool)
		for _, h := range hosts {
			pending[h.Name] = true
		}
		var mu sync.Mutex
		savePending := func() error {
			var names []string
			for _, h := range hosts {
				if pending[h.Name] {
					names = append(names, string(h.Name))
				}
			}
			tmp := filepath.Join(report, "pending.new")
			if err := os.WriteFile(tmp, []byte(strings.Join(names, "\n")+"\n"), 0600); err != nil {
				return err
			}
			return os.Rename(tmp, filepath.Join(report, "pending"))
		}
		if err := savePending(); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Staging %s: %d raw bytes; ready-host snapshot %s; %d hosts\n", sum, rawBytes, time.Now().UTC().Format(time.RFC3339), len(hosts))
		// A node offline for a whole day cannot keep an abandoned deploy helper forever. The 20-minute deploy wait is separate.
		ctx, cancel := context.WithTimeout(cmd.Context(), 24*time.Hour)
		defer cancel()
		var wg sync.WaitGroup
		for _, record := range hosts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				h, err := podman.New(record.SSH, runtimeDir, "stage-"+sum[:12]+"-"+string(record.Name), nil)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "stage %s: %v\n", record.Name, err)
					return
				}
				defer func() {
					cleanup, stop := context.WithTimeout(context.Background(), box.StartCleanupBudget)
					defer stop()
					h.Close(cleanup)
				}()
				for ctx.Err() == nil {
					progress := make(chan struct{}, 1)
					idle, stop := podman.IdleContext(ctx, box.StartStepBudget, progress)
					transfer := podman.WithByteProgress(idle, func(n int) {
						if n > 0 {
							select {
							case progress <- struct{}{}:
							default:
							}
						}
					})
					var encoding podman.BinaryEncoding
					payloadBytes := 0
					err = h.StageBinary(transfer, (box.Layout{HostData: hostData}).HostBin(), sum, func(codec podman.BinaryEncoding) ([]byte, error) {
						encoding = codec
						payload, err := cache.Bytes(codec)
						payloadBytes = len(payload)
						return payload, err
					})
					stop()
					if err == nil {
						mu.Lock()
						delete(pending, record.Name)
						saveErr := savePending()
						fmt.Fprintf(cmd.OutOrStdout(), "stage %s: staged %s in %s (%s, %d payload bytes; zero means already staged)\n", record.Name, sum, time.Since(start).Round(time.Millisecond), encoding, payloadBytes)
						mu.Unlock()
						if saveErr != nil {
							fmt.Fprintln(cmd.ErrOrStderr(), saveErr)
						}
						return
					}
					mu.Lock()
					fmt.Fprintf(cmd.ErrOrStderr(), "stage %s after %s: %v; retrying\n", record.Name, time.Since(start).Round(time.Second), err)
					mu.Unlock()
					select {
					case <-ctx.Done():
						return
					case <-time.After(30 * time.Second):
					}
				}
			}()
		}
		wg.Wait()
		if len(pending) > 0 {
			return fmt.Errorf("staging lifetime ended with %d hosts pending", len(pending))
		}
		return os.WriteFile(filepath.Join(report, "done"), []byte(sum+"\n"), 0600)
	}}
	cmd.Flags().StringVar(&data, "data", "", "Existing engine data folder (read only)")
	cmd.Flags().StringVar(&hostData, "host-data", "", "Host data folder")
	cmd.Flags().StringVar(&runtimeDir, "runtime", "", "Private staging SSH runtime folder")
	cmd.Flags().StringVar(&report, "report-dir", "", "Immutable deploy artifact and staging reports")
	return cmd
}
