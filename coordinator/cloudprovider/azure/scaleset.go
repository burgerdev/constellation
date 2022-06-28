package azure

import (
	"context"
	"errors"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/edgelesssys/constellation/coordinator/role"
	"github.com/edgelesssys/constellation/internal/azureshared"
	"github.com/edgelesssys/constellation/internal/cloud/metadata"
)

var (
	coordinatorScaleSetRegexp = regexp.MustCompile(`constellation-scale-set-coordinators-[0-9a-zA-Z]+$`)
	nodeScaleSetRegexp        = regexp.MustCompile(`constellation-scale-set-nodes-[0-9a-zA-Z]+$`)
)

// getScaleSetVM tries to get an azure vm belonging to a scale set.
func (m *Metadata) getScaleSetVM(ctx context.Context, providerID string) (metadata.InstanceMetadata, error) {
	_, resourceGroup, scaleSet, instanceID, err := azureshared.ScaleSetInformationFromProviderID(providerID)
	if err != nil {
		return metadata.InstanceMetadata{}, err
	}
	vmResp, err := m.virtualMachineScaleSetVMsAPI.Get(ctx, resourceGroup, scaleSet, instanceID, nil)
	if err != nil {
		return metadata.InstanceMetadata{}, err
	}
	networkInterfaces, err := m.getScaleSetVMInterfaces(ctx, vmResp.VirtualMachineScaleSetVM, resourceGroup, scaleSet, instanceID)
	if err != nil {
		return metadata.InstanceMetadata{}, err
	}
	publicIPAddresses, err := m.getScaleSetVMPublicIPAddresses(ctx, resourceGroup, scaleSet, instanceID, networkInterfaces)
	if err != nil {
		return metadata.InstanceMetadata{}, err
	}

	return convertScaleSetVMToCoreInstance(scaleSet, vmResp.VirtualMachineScaleSetVM, networkInterfaces, publicIPAddresses)
}

// listScaleSetVMs lists all scale set VMs in the current resource group.
func (m *Metadata) listScaleSetVMs(ctx context.Context, resourceGroup string) ([]metadata.InstanceMetadata, error) {
	instances := []metadata.InstanceMetadata{}
	scaleSetPager := m.scaleSetsAPI.List(resourceGroup, nil)
	for scaleSetPager.NextPage(ctx) {
		for _, scaleSet := range scaleSetPager.PageResponse().Value {
			if scaleSet == nil || scaleSet.Name == nil {
				continue
			}
			vmPager := m.virtualMachineScaleSetVMsAPI.List(resourceGroup, *scaleSet.Name, nil)
			for vmPager.NextPage(ctx) {
				for _, vm := range vmPager.PageResponse().Value {
					if vm == nil || vm.InstanceID == nil {
						continue
					}
					interfaces, err := m.getScaleSetVMInterfaces(ctx, *vm, resourceGroup, *scaleSet.Name, *vm.InstanceID)
					if err != nil {
						return nil, err
					}
					instance, err := convertScaleSetVMToCoreInstance(*scaleSet.Name, *vm, interfaces, nil)
					if err != nil {
						return nil, err
					}
					instances = append(instances, instance)
				}
			}
		}
	}
	return instances, nil
}

// convertScaleSetVMToCoreInstance converts an azure scale set virtual machine with interface configurations into a core.Instance.
func convertScaleSetVMToCoreInstance(scaleSet string, vm armcompute.VirtualMachineScaleSetVM, networkInterfaces []armnetwork.Interface, publicIPAddresses []string) (metadata.InstanceMetadata, error) {
	if vm.ID == nil {
		return metadata.InstanceMetadata{}, errors.New("retrieving instance from armcompute API client returned no instance ID")
	}
	if vm.Properties == nil || vm.Properties.OSProfile == nil || vm.Properties.OSProfile.ComputerName == nil {
		return metadata.InstanceMetadata{}, errors.New("retrieving instance from armcompute API client returned no computer name")
	}
	var sshKeys map[string][]string
	if vm.Properties.OSProfile.LinuxConfiguration == nil || vm.Properties.OSProfile.LinuxConfiguration.SSH == nil {
		sshKeys = map[string][]string{}
	} else {
		sshKeys = extractSSHKeys(*vm.Properties.OSProfile.LinuxConfiguration.SSH)
	}
	return metadata.InstanceMetadata{
		Name:       *vm.Properties.OSProfile.ComputerName,
		ProviderID: "azure://" + *vm.ID,
		Role:       extractScaleSetVMRole(scaleSet),
		PrivateIPs: extractPrivateIPs(networkInterfaces),
		PublicIPs:  publicIPAddresses,
		SSHKeys:    sshKeys,
	}, nil
}

// extractScaleSetVMRole extracts the constellation role of a scale set using its name.
func extractScaleSetVMRole(scaleSet string) role.Role {
	if coordinatorScaleSetRegexp.MatchString(scaleSet) {
		return role.Coordinator
	}
	if nodeScaleSetRegexp.MatchString(scaleSet) {
		return role.Node
	}
	return role.Unknown
}
