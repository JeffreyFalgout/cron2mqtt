package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/JeffreyFalgout/cron2mqtt/mqtt"
	"github.com/JeffreyFalgout/cron2mqtt/mqtt/hass"

	"github.com/spf13/cobra"
)

func init() {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Looks for cron jobs on MQTT that don't exist locally, then purges them from MQTT.",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			cl, err := mqtt.NewClient(c)
			if err != nil {
				return fmt.Errorf("could not initialize MQTT: %w", err)
			}
			defer cl.Close(250)
			ctx, canc := context.WithTimeout(context.Background(), timeout)
			defer canc()
			remote, err := discoverRemoteCronJobs(ctx, cl)
			if err != nil {
				return err
			}
			if len(remote) == 0 {
				fmt.Println("Discovered 0 cron jobs from MQTT.")
				return nil
			}
			var remoteCronJobIDs []string
			remoteCronJobByID := make(map[string]*hass.CronJob)
			for _, cj := range remote {
				remoteCronJobIDs = append(remoteCronJobIDs, cj.ID)
				remoteCronJobByID[cj.ID] = cj
			}
			sort.Strings(remoteCronJobIDs)

			fmt.Printf("Discovered %d cron jobs from MQTT:\n", len(remoteCronJobIDs))
			for _, id := range remoteCronJobIDs {
				fmt.Printf("  %s\n", id)
			}

			pcts, err := possibleCrontabs()
			if err != nil {
				return err
			}
			for _, pct := range pcts {
				fmt.Println()
				fmt.Printf("Checking %s...\n", pct.name())
				t, err := pct.load()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Could not load %s: %s\n", pct.name(), err)
					continue
				}

				for _, j := range t.Jobs() {
					if !j.Command.IsCron2Mqtt() {
						continue
					}

					for _, arg := range j.Command.Args() {
						if _, ok := remoteCronJobByID[arg]; ok {
							fmt.Println()
							fmt.Printf("  Discovered cron job %s locally:\n", arg)
							fmt.Printf("  $ %s\n", j.Command)
							delete(remoteCronJobByID, arg)
						}
					}
				}
			}

			for _, id := range remoteCronJobIDs {
				cj, ok := remoteCronJobByID[id]
				if !ok {
					continue
				}

				fmt.Println()
				fmt.Printf("Would you like to delete %s? [yN] ", cj.ID)
				var sel string
				fmt.Scanln(&sel)
				if strings.ToLower(sel) != "y" {
					continue
				}

				if err := cj.UnpublishConfig(); err != nil {
					fmt.Fprintf(os.Stderr, "Could not delete %s: %s\n", cj.ID, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 500*time.Millisecond, "The amount of time to spend discovering remote cron jobs.")
	rootCmd.AddCommand(cmd)
}

func discoverRemoteCronJobs(ctx context.Context, cl *mqtt.Client) ([]*hass.CronJob, error) {
	cjs := make(chan *hass.CronJob, 100)
	if err := hass.DiscoverCronJobs(ctx, cl, chan<- *hass.CronJob(cjs)); err != nil {
		return nil, err
	}

	var res []*hass.CronJob
	func() {
		for {
			select {
			case <-ctx.Done():
				return
			case cj, ok := <-cjs:
				if !ok {
					return
				}
				res = append(res, cj)
			}
		}
	}()

	return res, nil
}
