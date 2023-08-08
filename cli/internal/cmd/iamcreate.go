/*
Copyright (c) Edgeless Systems GmbH
SPDX-License-Identifier: AGPL-3.0-only
*/

package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/edgelesssys/constellation/v2/cli/internal/cloudcmd"
	"github.com/edgelesssys/constellation/v2/cli/internal/terraform"
	"github.com/edgelesssys/constellation/v2/internal/cloud/cloudprovider"
	"github.com/edgelesssys/constellation/v2/internal/config"
	"github.com/edgelesssys/constellation/v2/internal/constants"
	"github.com/edgelesssys/constellation/v2/internal/file"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
)

var (
	// GCP-specific validation regexes
	// Source: https://cloud.google.com/compute/docs/regions-zones
	zoneRegex   = regexp.MustCompile(`^\w+-\w+-[abc]$`)
	regionRegex = regexp.MustCompile(`^\w+-\w+[0-9]$`)
	// Source: https://cloud.google.com/resource-manager/reference/rest/v1/projects.
	gcpIDRegex = regexp.MustCompile(`^[a-z][-a-z0-9]{4,28}[a-z0-9]$`)
)

// NewIAMCmd returns a new cobra.Command for the iam parent command. It needs another verb and does nothing on its own.
func NewIAMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iam",
		Short: "Work with the IAM configuration on your cloud provider",
		Long:  "Work with the IAM configuration on your cloud provider.",
		Args:  cobra.ExactArgs(0),
	}

	cmd.AddCommand(newIAMCreateCmd())
	cmd.AddCommand(newIAMDestroyCmd())
	cmd.AddCommand(newIAMUpgradeCmd())
	return cmd
}

// NewIAMCreateCmd returns a new cobra.Command for the iam create parent command. It needs another verb, and does nothing on its own.
func newIAMCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create IAM configuration on a cloud platform for your Constellation cluster",
		Long:  "Create IAM configuration on a cloud platform for your Constellation cluster.",
		Args:  cobra.ExactArgs(0),
	}

	cmd.PersistentFlags().BoolP("yes", "y", false, "create the IAM configuration without further confirmation")
	cmd.PersistentFlags().Bool("update-config", false, "update the config file with the specific IAM information")

	cmd.AddCommand(newIAMCreateAWSCmd())
	cmd.AddCommand(newIAMCreateAzureCmd())
	cmd.AddCommand(newIAMCreateGCPCmd())

	return cmd
}

// newIAMCreateAWSCmd returns a new cobra.Command for the iam create aws command.
func newIAMCreateAWSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aws",
		Short: "Create IAM configuration on AWS for your Constellation cluster",
		Long:  "Create IAM configuration on AWS for your Constellation cluster.",
		Args:  cobra.ExactArgs(0),
		RunE:  createRunIAMFunc(cloudprovider.AWS),
	}

	cmd.Flags().String("prefix", "", "name prefix for all resources (required)")
	must(cobra.MarkFlagRequired(cmd.Flags(), "prefix"))
	cmd.Flags().String("zone", "", "AWS availability zone the resources will be created in, e.g., us-east-2a (required)\n"+
		"See the Constellation docs for a list of currently supported regions.")
	must(cobra.MarkFlagRequired(cmd.Flags(), "zone"))
	return cmd
}

// newIAMCreateAzureCmd returns a new cobra.Command for the iam create azure command.
func newIAMCreateAzureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "azure",
		Short: "Create IAM configuration on Microsoft Azure for your Constellation cluster",
		Long:  "Create IAM configuration on Microsoft Azure for your Constellation cluster.",
		Args:  cobra.ExactArgs(0),
		RunE:  createRunIAMFunc(cloudprovider.Azure),
	}

	cmd.Flags().String("resourceGroup", "", "name prefix of the two resource groups your cluster / IAM resources will be created in (required)")
	must(cobra.MarkFlagRequired(cmd.Flags(), "resourceGroup"))
	cmd.Flags().String("region", "", "region the resources will be created in, e.g., westus (required)")
	must(cobra.MarkFlagRequired(cmd.Flags(), "region"))
	cmd.Flags().String("servicePrincipal", "", "name of the service principal that will be created (required)")
	must(cobra.MarkFlagRequired(cmd.Flags(), "servicePrincipal"))
	return cmd
}

// NewIAMCreateGCPCmd returns a new cobra.Command for the iam create gcp command.
func newIAMCreateGCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gcp",
		Short: "Create IAM configuration on GCP for your Constellation cluster",
		Long:  "Create IAM configuration on GCP for your Constellation cluster.",
		Args:  cobra.ExactArgs(0),
		RunE:  createRunIAMFunc(cloudprovider.GCP),
	}

	cmd.Flags().String("zone", "", "GCP zone the cluster will be deployed in (required)\n"+
		"Find a list of available zones here: https://cloud.google.com/compute/docs/regions-zones#available")
	must(cobra.MarkFlagRequired(cmd.Flags(), "zone"))
	cmd.Flags().String("serviceAccountID", "", "ID for the service account that will be created (required)\n"+
		"Must be 6 to 30 lowercase letters, digits, or hyphens.")
	must(cobra.MarkFlagRequired(cmd.Flags(), "serviceAccountID"))
	cmd.Flags().String("projectID", "", "ID of the GCP project the configuration will be created in (required)\n"+
		"Find it on the welcome screen of your project: https://console.cloud.google.com/welcome")
	must(cobra.MarkFlagRequired(cmd.Flags(), "projectID"))

	return cmd
}

// createRunIAMFunc is the entrypoint for the iam create command. It sets up the iamCreator
// and starts IAM creation for the specific cloud provider.
func createRunIAMFunc(provider cloudprovider.Provider) func(cmd *cobra.Command, args []string) error {
	var providerCreator func(workspace string) providerIAMCreator
	switch provider {
	case cloudprovider.AWS:
		providerCreator = func(string) providerIAMCreator { return &awsIAMCreator{} }
	case cloudprovider.Azure:
		providerCreator = func(string) providerIAMCreator { return &azureIAMCreator{} }
	case cloudprovider.GCP:
		providerCreator = func(workspace string) providerIAMCreator {
			return &gcpIAMCreator{workspace}
		}
	default:
		return func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("unknown provider %s", provider)
		}
	}

	return func(cmd *cobra.Command, args []string) error {
		logLevelString, err := cmd.Flags().GetString("tf-log")
		if err != nil {
			return fmt.Errorf("parsing tf-log string: %w", err)
		}
		logLevel, err := terraform.ParseLogLevel(logLevelString)
		if err != nil {
			return fmt.Errorf("parsing Terraform log level %s: %w", logLevelString, err)
		}
		workspace, err := cmd.Flags().GetString("workspace")
		if err != nil {
			return fmt.Errorf("parsing workspace string: %w", err)
		}

		iamCreator, err := newIAMCreator(cmd, workspace, logLevel)
		if err != nil {
			return fmt.Errorf("creating iamCreator: %w", err)
		}
		defer iamCreator.spinner.Stop()
		defer iamCreator.log.Sync()
		iamCreator.provider = provider
		iamCreator.providerCreator = providerCreator(workspace)
		return iamCreator.create(cmd.Context())
	}
}

// newIAMCreator creates a new iamiamCreator.
func newIAMCreator(cmd *cobra.Command, workspace string, logLevel terraform.LogLevel) (*iamCreator, error) {
	spinner, err := newSpinnerOrStderr(cmd)
	if err != nil {
		return nil, fmt.Errorf("creating spinner: %w", err)
	}
	log, err := newCLILogger(cmd)
	if err != nil {
		return nil, fmt.Errorf("creating logger: %w", err)
	}
	log.Debugf("Terraform logs will be written into %s at level %s", terraformLogPath(workspace), logLevel.String())

	return &iamCreator{
		cmd:         cmd,
		spinner:     spinner,
		log:         log,
		creator:     cloudcmd.NewIAMCreator(spinner),
		fileHandler: file.NewHandler(afero.NewOsFs()),
		iamConfig: &cloudcmd.IAMConfigOptions{
			TFWorkspace: constants.TerraformIAMWorkingDir,
			TFLogLevel:  logLevel,
		},
	}, nil
}

// iamCreator is the iamCreator for the iam create command.
type iamCreator struct {
	cmd             *cobra.Command
	spinner         spinnerInterf
	creator         cloudIAMCreator
	fileHandler     file.Handler
	provider        cloudprovider.Provider
	providerCreator providerIAMCreator
	iamConfig       *cloudcmd.IAMConfigOptions
	log             debugLog
}

// create IAM configuration on the iamCreator's cloud provider.
func (c *iamCreator) create(ctx context.Context) error {
	flags, err := c.parseFlagsAndSetupConfig()
	if err != nil {
		return err
	}
	c.log.Debugf("Using flags: %+v", flags)

	if err := c.checkWorkingDir(flags.workspace); err != nil {
		return err
	}

	if !flags.yesFlag {
		c.cmd.Printf("The following IAM configuration will be created:\n\n")
		c.providerCreator.printConfirmValues(c.cmd, flags)
		ok, err := askToConfirm(c.cmd, "Do you want to create the configuration?")
		if err != nil {
			return err
		}
		if !ok {
			c.cmd.Println("The creation of the configuration was aborted.")
			return nil
		}
	}

	var conf config.Config
	if flags.updateConfig {
		c.log.Debugf("Parsing config %s", configPath(flags.workspace))
		if err = c.fileHandler.ReadYAML(constants.ConfigFilename, &conf); err != nil {
			return fmt.Errorf("error reading the configuration file: %w", err)
		}
		if err := validateConfigWithFlagCompatibility(c.provider, conf, flags); err != nil {
			return err
		}
		c.cmd.Printf("The configuration file %q will be automatically updated with the IAM values and zone/region information.\n", configPath(flags.workspace))
	}

	c.spinner.Start("Creating", false)
	iamFile, err := c.creator.Create(ctx, c.provider, c.iamConfig)
	c.spinner.Stop()
	if err != nil {
		return err
	}
	c.cmd.Println() // Print empty line to separate after spinner ended.
	c.log.Debugf("Successfully created the IAM cloud resources")

	err = c.providerCreator.parseAndWriteIDFile(iamFile, c.fileHandler)
	if err != nil {
		return err
	}

	if flags.updateConfig {
		c.log.Debugf("Writing IAM configuration to %s", configPath(flags.workspace))
		c.providerCreator.writeOutputValuesToConfig(&conf, flags, iamFile)
		if err := c.fileHandler.WriteYAML(constants.ConfigFilename, conf, file.OptOverwrite); err != nil {
			return err
		}
		c.cmd.Printf("Your IAM configuration was created and filled into %s successfully.\n", configPath(flags.workspace))
		return nil
	}

	c.providerCreator.printOutputValues(c.cmd, flags, iamFile)
	c.cmd.Println("Your IAM configuration was created successfully. Please fill the above values into your configuration file.")

	return nil
}

// parseFlagsAndSetupConfig parses the flags of the iam create command and fills the values into the IAM config (output values of the command).
func (c *iamCreator) parseFlagsAndSetupConfig() (iamFlags, error) {
	cwd, err := c.cmd.Flags().GetString("workspace")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing config string: %w", err)
	}
	yesFlag, err := c.cmd.Flags().GetBool("yes")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing yes bool: %w", err)
	}
	updateConfig, err := c.cmd.Flags().GetBool("update-config")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing update-config bool: %w", err)
	}

	flags := iamFlags{
		workspace:    cwd,
		yesFlag:      yesFlag,
		updateConfig: updateConfig,
	}

	flags, err = c.providerCreator.parseFlagsAndSetupConfig(c.cmd, flags, c.iamConfig)
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing provider-specific value: %w", err)
	}

	return flags, nil
}

// checkWorkingDir checks if the current working directory already contains a Terraform dir.
func (c *iamCreator) checkWorkingDir(workspace string) error {
	if _, err := c.fileHandler.Stat(constants.TerraformIAMWorkingDir); err == nil {
		return fmt.Errorf("the current working directory already contains the Terraform workspace directory %q. Please run the command in a different directory or destroy the existing workspace", terraformIAMWorkspace(workspace))
	}
	return nil
}

// iamFlags contains the parsed flags of the iam create command, including the parsed flags of the selected cloud provider.
type iamFlags struct {
	aws          awsFlags
	azure        azureFlags
	gcp          gcpFlags
	workspace    string
	yesFlag      bool
	updateConfig bool
}

// awsFlags contains the parsed flags of the iam create aws command.
type awsFlags struct {
	prefix string
	region string
	zone   string
}

// azureFlags contains the parsed flags of the iam create azure command.
type azureFlags struct {
	region           string
	resourceGroup    string
	servicePrincipal string
}

// gcpFlags contains the parsed flags of the iam create gcp command.
type gcpFlags struct {
	serviceAccountID string
	zone             string
	region           string
	projectID        string
}

// providerIAMCreator is an interface for the IAM actions of different cloud providers.
type providerIAMCreator interface {
	// printConfirmValues prints the values that will be created on the cloud provider and need to be confirmed by the user.
	printConfirmValues(cmd *cobra.Command, flags iamFlags)
	// printOutputValues prints the values that were created on the cloud provider.
	printOutputValues(cmd *cobra.Command, flags iamFlags, iamFile cloudcmd.IAMOutput)
	// writeOutputValuesToConfig writes the output values of the IAM creation to the constellation config file.
	writeOutputValuesToConfig(conf *config.Config, flags iamFlags, iamFile cloudcmd.IAMOutput)
	// parseFlagsAndSetupConfig parses the provider-specific flags and fills the values into the IAM config (output values of the command).
	parseFlagsAndSetupConfig(cmd *cobra.Command, flags iamFlags, iamConfig *cloudcmd.IAMConfigOptions) (iamFlags, error)
	// parseAndWriteIDFile parses the GCP service account key and writes it to a keyfile. It is only implemented for GCP.
	parseAndWriteIDFile(iamFile cloudcmd.IAMOutput, fileHandler file.Handler) error
}

// awsIAMCreator implements the providerIAMCreator interface for AWS.
type awsIAMCreator struct{}

func (c *awsIAMCreator) parseFlagsAndSetupConfig(cmd *cobra.Command, flags iamFlags, iamConfig *cloudcmd.IAMConfigOptions) (iamFlags, error) {
	prefix, err := cmd.Flags().GetString("prefix")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing prefix string: %w", err)
	}
	if len(prefix) > 36 {
		return iamFlags{}, fmt.Errorf("prefix must be 36 characters or less")
	}
	zone, err := cmd.Flags().GetString("zone")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing zone string: %w", err)
	}

	if !config.ValidateAWSZone(zone) {
		return iamFlags{}, fmt.Errorf("invalid AWS zone. To find a valid zone, please refer to our docs and https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/using-regions-availability-zones.html#concepts-availability-zones")
	}
	// Infer region from zone.
	region := zone[:len(zone)-1]
	if !config.ValidateAWSRegion(region) {
		return iamFlags{}, fmt.Errorf("invalid AWS region: %s", region)
	}

	flags.aws = awsFlags{
		prefix: prefix,
		zone:   zone,
		region: region,
	}

	// Setup IAM config.
	iamConfig.AWS = cloudcmd.AWSIAMConfig{
		Region: flags.aws.region,
		Prefix: flags.aws.prefix,
	}

	return flags, nil
}

func (c *awsIAMCreator) printConfirmValues(cmd *cobra.Command, flags iamFlags) {
	cmd.Printf("Region:\t\t%s\n", flags.aws.region)
	cmd.Printf("Name Prefix:\t%s\n\n", flags.aws.prefix)
}

func (c *awsIAMCreator) printOutputValues(cmd *cobra.Command, flags iamFlags, iamFile cloudcmd.IAMOutput) {
	cmd.Printf("region:\t\t\t%s\n", flags.aws.region)
	cmd.Printf("zone:\t\t\t%s\n", flags.aws.zone)
	cmd.Printf("iamProfileControlPlane:\t%s\n", iamFile.AWSOutput.ControlPlaneInstanceProfile)
	cmd.Printf("iamProfileWorkerNodes:\t%s\n\n", iamFile.AWSOutput.WorkerNodeInstanceProfile)
}

func (c *awsIAMCreator) writeOutputValuesToConfig(conf *config.Config, flags iamFlags, iamFile cloudcmd.IAMOutput) {
	conf.Provider.AWS.Region = flags.aws.region
	conf.Provider.AWS.Zone = flags.aws.zone
	conf.Provider.AWS.IAMProfileControlPlane = iamFile.AWSOutput.ControlPlaneInstanceProfile
	conf.Provider.AWS.IAMProfileWorkerNodes = iamFile.AWSOutput.WorkerNodeInstanceProfile
	for groupName, group := range conf.NodeGroups {
		group.Zone = flags.aws.zone
		conf.NodeGroups[groupName] = group
	}
}

func (c *awsIAMCreator) parseAndWriteIDFile(_ cloudcmd.IAMOutput, _ file.Handler) error {
	return nil
}

// azureIAMCreator implements the providerIAMCreator interface for Azure.
type azureIAMCreator struct{}

func (c *azureIAMCreator) parseFlagsAndSetupConfig(cmd *cobra.Command, flags iamFlags, iamConfig *cloudcmd.IAMConfigOptions) (iamFlags, error) {
	region, err := cmd.Flags().GetString("region")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing region string: %w", err)
	}

	resourceGroup, err := cmd.Flags().GetString("resourceGroup")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing resourceGroup string: %w", err)
	}

	servicePrincipal, err := cmd.Flags().GetString("servicePrincipal")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing servicePrincipal string: %w", err)
	}

	flags.azure = azureFlags{
		region:           region,
		resourceGroup:    resourceGroup,
		servicePrincipal: servicePrincipal,
	}

	// Setup IAM config.
	iamConfig.Azure = cloudcmd.AzureIAMConfig{
		Region:           flags.azure.region,
		ResourceGroup:    flags.azure.resourceGroup,
		ServicePrincipal: flags.azure.servicePrincipal,
	}

	return flags, nil
}

func (c *azureIAMCreator) printConfirmValues(cmd *cobra.Command, flags iamFlags) {
	cmd.Printf("Region:\t\t\t%s\n", flags.azure.region)
	cmd.Printf("Resource Group:\t\t%s\n", flags.azure.resourceGroup)
	cmd.Printf("Service Principal:\t%s\n\n", flags.azure.servicePrincipal)
}

func (c *azureIAMCreator) printOutputValues(cmd *cobra.Command, flags iamFlags, iamFile cloudcmd.IAMOutput) {
	cmd.Printf("subscription:\t\t%s\n", iamFile.AzureOutput.SubscriptionID)
	cmd.Printf("tenant:\t\t\t%s\n", iamFile.AzureOutput.TenantID)
	cmd.Printf("location:\t\t%s\n", flags.azure.region)
	cmd.Printf("resourceGroup:\t\t%s\n", flags.azure.resourceGroup)
	cmd.Printf("userAssignedIdentity:\t%s\n", iamFile.AzureOutput.UAMIID)
}

func (c *azureIAMCreator) writeOutputValuesToConfig(conf *config.Config, flags iamFlags, iamFile cloudcmd.IAMOutput) {
	conf.Provider.Azure.SubscriptionID = iamFile.AzureOutput.SubscriptionID
	conf.Provider.Azure.TenantID = iamFile.AzureOutput.TenantID
	conf.Provider.Azure.Location = flags.azure.region
	conf.Provider.Azure.ResourceGroup = flags.azure.resourceGroup
	conf.Provider.Azure.UserAssignedIdentity = iamFile.AzureOutput.UAMIID
}

func (c *azureIAMCreator) parseAndWriteIDFile(_ cloudcmd.IAMOutput, _ file.Handler) error {
	return nil
}

// gcpIAMCreator implements the providerIAMCreator interface for GCP.
type gcpIAMCreator struct {
	workspace string
}

func (c *gcpIAMCreator) parseFlagsAndSetupConfig(cmd *cobra.Command, flags iamFlags, iamConfig *cloudcmd.IAMConfigOptions) (iamFlags, error) {
	zone, err := cmd.Flags().GetString("zone")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing zone string: %w", err)
	}
	if !zoneRegex.MatchString(zone) {
		return iamFlags{}, fmt.Errorf("invalid zone string: %s", zone)
	}

	// Infer region from zone.
	zoneParts := strings.Split(zone, "-")
	region := fmt.Sprintf("%s-%s", zoneParts[0], zoneParts[1])
	if !regionRegex.MatchString(region) {
		return iamFlags{}, fmt.Errorf("invalid region string: %s", region)
	}

	projectID, err := cmd.Flags().GetString("projectID")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing projectID string: %w", err)
	}
	if !gcpIDRegex.MatchString(projectID) {
		return iamFlags{}, fmt.Errorf("projectID %q doesn't match %s", projectID, gcpIDRegex)
	}

	serviceAccID, err := cmd.Flags().GetString("serviceAccountID")
	if err != nil {
		return iamFlags{}, fmt.Errorf("parsing serviceAccountID string: %w", err)
	}
	if !gcpIDRegex.MatchString(serviceAccID) {
		return iamFlags{}, fmt.Errorf("serviceAccountID %q doesn't match %s", serviceAccID, gcpIDRegex)
	}

	flags.gcp = gcpFlags{
		zone:             zone,
		region:           region,
		projectID:        projectID,
		serviceAccountID: serviceAccID,
	}

	// Setup IAM config.
	iamConfig.GCP = cloudcmd.GCPIAMConfig{
		Zone:             flags.gcp.zone,
		Region:           flags.gcp.region,
		ProjectID:        flags.gcp.projectID,
		ServiceAccountID: flags.gcp.serviceAccountID,
	}

	return flags, nil
}

func (c *gcpIAMCreator) printConfirmValues(cmd *cobra.Command, flags iamFlags) {
	cmd.Printf("Project ID:\t\t%s\n", flags.gcp.projectID)
	cmd.Printf("Service Account ID:\t%s\n", flags.gcp.serviceAccountID)
	cmd.Printf("Region:\t\t\t%s\n", flags.gcp.region)
	cmd.Printf("Zone:\t\t\t%s\n\n", flags.gcp.zone)
}

func (c *gcpIAMCreator) printOutputValues(cmd *cobra.Command, flags iamFlags, _ cloudcmd.IAMOutput) {
	cmd.Printf("projectID:\t\t%s\n", flags.gcp.projectID)
	cmd.Printf("region:\t\t\t%s\n", flags.gcp.region)
	cmd.Printf("zone:\t\t\t%s\n", flags.gcp.zone)
	cmd.Printf("serviceAccountKeyPath:\t%s\n\n", gcpServiceAccountKeyPath(c.workspace))
}

func (c *gcpIAMCreator) writeOutputValuesToConfig(conf *config.Config, flags iamFlags, _ cloudcmd.IAMOutput) {
	conf.Provider.GCP.Project = flags.gcp.projectID
	conf.Provider.GCP.ServiceAccountKeyPath = gcpServiceAccountKeyFile // File was created in workspace, so only the filename is needed.
	conf.Provider.GCP.Region = flags.gcp.region
	conf.Provider.GCP.Zone = flags.gcp.zone
	for groupName, group := range conf.NodeGroups {
		group.Zone = flags.gcp.zone
		conf.NodeGroups[groupName] = group
	}
}

func (c *gcpIAMCreator) parseAndWriteIDFile(iamFile cloudcmd.IAMOutput, fileHandler file.Handler) error {
	// GCP needs to write the service account key to a file.
	tmpOut, err := parseIDFile(iamFile.GCPOutput.ServiceAccountKey)
	if err != nil {
		return err
	}

	return fileHandler.WriteJSON(gcpServiceAccountKeyFile, tmpOut, file.OptNone)
}

// parseIDFile parses the given base64 encoded JSON string of the GCP service account key and returns a map.
func parseIDFile(serviceAccountKeyBase64 string) (map[string]string, error) {
	dec, err := base64.StdEncoding.DecodeString(serviceAccountKeyBase64)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string)
	if err = json.Unmarshal(dec, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateConfigWithFlagCompatibility checks if the config is compatible with the flags.
func validateConfigWithFlagCompatibility(iamProvider cloudprovider.Provider, cfg config.Config, flags iamFlags) error {
	if !cfg.HasProvider(iamProvider) {
		return fmt.Errorf("cloud provider from the the configuration file differs from the one provided via the command %q", iamProvider)
	}
	return checkIfCfgZoneAndFlagZoneDiffer(iamProvider, flags, cfg)
}

func checkIfCfgZoneAndFlagZoneDiffer(iamProvider cloudprovider.Provider, flags iamFlags, cfg config.Config) error {
	flagZone := flagZoneOrAzRegion(iamProvider, flags)
	configZone := cfg.GetZone()
	if configZone != "" && flagZone != configZone {
		return fmt.Errorf("zone/region from the configuration file %q differs from the one provided via flags %q", configZone, flagZone)
	}
	return nil
}

func flagZoneOrAzRegion(provider cloudprovider.Provider, flags iamFlags) string {
	switch provider {
	case cloudprovider.AWS:
		return flags.aws.zone
	case cloudprovider.Azure:
		return flags.azure.region
	case cloudprovider.GCP:
		return flags.gcp.zone
	}
	return ""
}
