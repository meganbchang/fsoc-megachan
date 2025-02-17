// Copyright 2023 Cisco Systems, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package optimize

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/apex/log"
	"github.com/spf13/cobra"

	"github.com/cisco-open/fsoc/cmd/config"
	"github.com/cisco-open/fsoc/output"
	"github.com/cisco-open/fsoc/platform/api"
)

// flags common to all commands in this file (TODO could probably extend this to other commands)
type managementFlags struct {
	cluster      string
	namespace    string
	workloadName string
	optimizerId  string
	solutionName string
}

func (flags *managementFlags) addCommonFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&flags.cluster, "cluster", "c", "", "Manage optimization for a workload with this cluster name")
	cmd.Flags().StringVarP(&flags.namespace, "namespace", "n", "", "Manage optimization for a workload with this kubernetes namespace")
	cmd.Flags().StringVarP(&flags.workloadName, "workload-name", "w", "", "Manage optimization for a workload with this name in its kubernetes manifest")
	cmd.MarkFlagsRequiredTogether("cluster", "namespace", "workload-name")

	cmd.Flags().StringVarP(&flags.optimizerId, "optimizer-id", "i", "", "Manage a specific optimizer by its ID")
	cmd.MarkFlagsMutuallyExclusive("optimizer-id", "cluster")
	cmd.MarkFlagsMutuallyExclusive("optimizer-id", "namespace")
	cmd.MarkFlagsMutuallyExclusive("optimizer-id", "workload-name")

	cmd.Flags().StringVarP(&flags.solutionName, "solution-name", "", "optimize", "Intended for developer usage, overrides the name of the solution defining the Orion types for reading/writing")
	if err := cmd.LocalFlags().MarkHidden("solution-name"); err != nil {
		log.Warnf("Failed to set solution-name flag hidden: %v", err)
	}
}

func (flags *managementFlags) getOptimizerConfig() (OptimizerConfiguration, error) {
	optimizerConfig := OptimizerConfiguration{}
	headers := getOrionTenantHeaders()
	if flags.optimizerId != "" {
		var response configJsonStoreItem

		urlStr := fmt.Sprintf("objstore/v1beta/objects/%v:optimizer/%v", flags.solutionName, flags.optimizerId)
		err := api.JSONGet(urlStr, &response, &api.Options{Headers: headers})
		if err != nil {
			return optimizerConfig, fmt.Errorf("Unable to fetch config by optimizer ID. api.JSONGet: %w", err)
		}

		optimizerConfig = response.Data
	} else if flags.cluster != "" {
		var configPage configJsonStorePage
		// NOTE orion objects only store the last portion of the workloadId. Only support k8sDeployment currently
		queryStr := url.QueryEscape(fmt.Sprintf(
			"data.target.k8sDeployment.clusterName eq %q"+
				" and data.target.k8sDeployment.namespaceName eq %q"+
				" and data.target.k8sDeployment.workloadName eq %q",
			flags.cluster, flags.namespace, flags.workloadName))
		urlStr := fmt.Sprintf("objstore/v1beta/objects/%v:optimizer?filter=%v", flags.solutionName, queryStr)

		err := api.JSONGet(urlStr, &configPage, &api.Options{Headers: headers})
		if err != nil {
			return optimizerConfig, fmt.Errorf("unable to fetch config by workload information. api.JSONGet: %w", err)
		}
		if configPage.Total != 1 {
			return optimizerConfig, fmt.Errorf("Found %v optimizer configurations for the given workload information", configPage.Total)
		}

		optimizerConfig = configPage.Items[0].Data
	} else {
		return optimizerConfig, errors.New("No identifying information provided for the optimizer to be managed")
	}

	return optimizerConfig, nil
}

func (flags *managementFlags) updateOptimizerConfiguration(config OptimizerConfiguration) error {
	var res any
	urlStr := fmt.Sprintf("objstore/v1beta/objects/%v:optimizer/%v", flags.solutionName, config.OptimizerID)
	if err := api.JSONPut(urlStr, config, &res, &api.Options{Headers: getOrionTenantHeaders()}); err != nil {
		return fmt.Errorf("Failed to update knowledge object with new optimizer configuration. api.JSONPut: %w", err)
	}
	return nil
}

type startFlags struct {
	managementFlags
	restart bool
}

func init() {
	// TODO move this logic to optimize root when implementing unit tests
	optimizeCmd.AddCommand(NewCmdStart())
	optimizeCmd.AddCommand(NewCmdStop())
	optimizeCmd.AddCommand(NewCmdSuspend())
	optimizeCmd.AddCommand(NewCmdUnsuspend())
}

func NewCmdStart() *cobra.Command {
	flags := startFlags{}
	command := &cobra.Command{
		Use:   "start",
		Short: "(Re)Start an optimizer",
		Example: `  fsoc optimize start --cluster your-cluster --namespace your-namespace --workload-name your-workload
  fsoc optimize start --optimizer-id namespace-name-00000000-0000-0000-0000-000000000000`,
		Args:             cobra.NoArgs,
		RunE:             startOptimizer(&flags),
		TraverseChildren: true,
	}
	flags.addCommonFlags(command)
	command.Flags().BoolVarP(&flags.restart, "restart", "r", false, "Restart the optimization if already started")
	return command
}

func startOptimizer(flags *startFlags) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		config, err := flags.getOptimizerConfig()
		if err != nil {
			return fmt.Errorf("flags.getOptimizerConfig: %w", err)
		}
		if config.DesiredState == "started" {
			if !flags.restart {
				return errors.New("Optimizer already started (did you mean to specify --restart?)")
			}
		}

		config.DesiredState = "started"
		config.RestartTimestamp = time.Now().UTC().String()

		if err := flags.updateOptimizerConfiguration(config); err != nil {
			return fmt.Errorf("flags.updateOptimizerConfiguration: %w", err)
		}
		output.PrintCmdStatus(cmd, fmt.Sprintf("Optimizer %q started\n", config.OptimizerID))
		return nil
	}
}

func NewCmdStop() *cobra.Command {
	flags := managementFlags{}
	command := &cobra.Command{
		Use:   "stop",
		Short: "Stop an optimizer",
		Example: `  fsoc optimize stop --cluster your-cluster --namespace your-namespace --workload-name your-workload
  fsoc optimize stop --optimizer-id namespace-name-00000000-0000-0000-0000-000000000000`,
		Args:             cobra.NoArgs,
		RunE:             stopOptimizer(&flags),
		TraverseChildren: true,
	}
	flags.addCommonFlags(command)
	return command
}

func stopOptimizer(flags *managementFlags) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		config, err := flags.getOptimizerConfig()
		if err != nil {
			return fmt.Errorf("flags.getOptimizerConfig: %w", err)
		}
		if config.DesiredState == "stopped" {
			return errors.New("Optimizer already stopped")
		}

		config.DesiredState = "stopped"

		if err := flags.updateOptimizerConfiguration(config); err != nil {
			return fmt.Errorf("flags.updateOptimizerConfiguration: %w", err)
		}
		output.PrintCmdStatus(cmd, fmt.Sprintf("Optimizer %q stopped\n", config.OptimizerID))
		return nil
	}
}

type suspendFlags struct {
	managementFlags
	suspensionId string
	reason       string
}

func NewCmdSuspend() *cobra.Command {
	flags := suspendFlags{}
	command := &cobra.Command{
		Use:   "suspend",
		Short: "Suspend an optimization",
		Long: `
Raise a flag on the optimizer configuration to halt optimization activity. 

Unlike stop, suspension is meant to be temporary and will allow optimization to resume at a given step instead of
discarding the run. Suspensions are also additive; Multiple suspensions can be active at any given time and
optimization will not proceed until all suspensions are removed.`,
		Example: `  fsoc optimize suspend --reason "Pausing for CICD blackout" --cluster your-cluster --namespace your-namespace --workload-name your-workload
  fsoc optimize suspend --reason "Pausing for CICD blackout" --optimizer-id namespace-name-00000000-0000-0000-0000-000000000000`,
		Args:             cobra.NoArgs,
		RunE:             suspendOptimizer(&flags),
		TraverseChildren: true,
	}
	flags.addCommonFlags(command)
	command.Flags().StringVarP(&flags.suspensionId, "suspension-id", "s", "userPause", "Shorthand identifier for the suspension being added")
	command.Flags().StringVarP(&flags.reason, "reason", "r", "", "Long form explanation text of why the optimization is suspended")
	if err := command.MarkFlagRequired("reason"); err != nil {
		log.Warnf("Failed to set reason flag required: %v", err)
	}
	return command
}

func suspendOptimizer(flags *suspendFlags) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		optimizerConfig, err := flags.getOptimizerConfig()
		if err != nil {
			return fmt.Errorf("flags.getOptimizerConfig: %w", err)
		}

		if optimizerConfig.Suspensions == nil {
			optimizerConfig.Suspensions = make(map[string]Suspension)
		}
		if _, ok := optimizerConfig.Suspensions[flags.suspensionId]; ok {
			return fmt.Errorf(
				"Optimizer configuration already has suspension with ID %q. "+
					"Please use a different suspension ID to avoid removing a suspension you may not have added",
				flags.suspensionId,
			)
		}

		newSuspension := Suspension{Reason: flags.reason}
		newSuspension.Timestamp = time.Now().UTC().String()
		newSuspension.User = config.GetCurrentContext().User
		optimizerConfig.Suspensions[flags.suspensionId] = newSuspension

		if err := flags.updateOptimizerConfiguration(optimizerConfig); err != nil {
			return fmt.Errorf("flags.updateOptimizerConfiguration: %w", err)
		}
		output.PrintCmdStatus(cmd, fmt.Sprintf("Suspension added to optimizer %q\n", optimizerConfig.OptimizerID))
		return nil
	}
}

func NewCmdUnsuspend() *cobra.Command {
	flags := suspendFlags{}
	command := &cobra.Command{
		Use:   "unsuspend",
		Short: "Unsuspend an optimization",
		Example: `  fsoc optimize unsuspend --cluster your-cluster --namespace your-namespace --workload-name your-workload
  fsoc optimize unsuspend --optimizer-id namespace-name-00000000-0000-0000-0000-000000000000`,
		Args:             cobra.NoArgs,
		RunE:             unsuspendOptimizer(&flags),
		TraverseChildren: true,
	}
	flags.addCommonFlags(command)
	command.Flags().StringVarP(&flags.suspensionId, "suspension-id", "s", "userPause", "Shorthand identifier for the suspension being removed")
	return command
}

func unsuspendOptimizer(flags *suspendFlags) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		config, err := flags.getOptimizerConfig()
		if err != nil {
			return fmt.Errorf("flags.getOptimizerConfig: %w", err)
		}

		if config.Suspensions == nil || len(config.Suspensions) < 1 {
			return errors.New("Optimizer has no suspensions to remove")
		}
		if _, ok := config.Suspensions[flags.suspensionId]; !ok {
			return fmt.Errorf("Optimizer has no suspension with ID %q to be removed", flags.suspensionId)
		}
		delete(config.Suspensions, flags.suspensionId)

		if err := flags.updateOptimizerConfiguration(config); err != nil {
			return fmt.Errorf("flags.updateOptimizerConfiguration: %w", err)
		}
		output.PrintCmdStatus(cmd, fmt.Sprintf("Suspension removed for optimizer %q\n", config.OptimizerID))
		return nil
	}
}
