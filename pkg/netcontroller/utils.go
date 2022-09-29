package netcontroller

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"strings"

	"github.com/nokia/net-attach-def-admission-controller/pkg/vlanprovider"
	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"

	"k8s.io/klog"
)

const (
	sriovConfigFile = "/etc/pcidp/config.json"
)

type SriovResourceList struct {
	Resources []SriovResource `json:"resourceList"`
}

type SriovResource struct {
	ResourceName string         `json:"resourceName"`
	Selectors    SriovSelectors `json:"selectors"`
}

type SriovSelectors struct {
	PCIAddresses []string `json:"pciAddresses,omitempty"`
	Drivers      []string `json:"drivers,omitempty"`
	PFNames      []string `json:"pfNames,omitempty"`
}

func getVlanInterface(vlanIfName string) bool {
	_, err := netlink.LinkByName(vlanIfName)
	if err == nil {
		return true
	}
	return false
}

func createVlanInterface(vlanIfName string, vlanId int) error {
	// Check if vlan interface already exists
	if getVlanInterface(vlanIfName) {
		klog.Info("requested vlan interface already exists")
		return nil
	}
	m := strings.Split(vlanIfName, ".")
	// Check if vlanId is already used
	vlanByOther := "vlan" + m[1]
	_, err := netlink.LinkByName(vlanByOther)
	if err == nil {
		return errors.New("requested vlan is already used by other function")
	}
	// Check if master exists
	link, err := netlink.LinkByName(m[0] + "-bond")
	if err != nil {
		return err
	}
	// Create the vlan interface
	vlan := netlink.Vlan{}
	vlan.ParentIndex = link.Attrs().Index
	vlan.Name = vlanIfName
	vlan.VlanId = vlanId
	err = netlink.LinkAdd(&vlan)
	if err != nil {
		return err
	}
	err = netlink.LinkSetUp(&vlan)
	if err != nil {
		return err
	}
	return nil
}

func deleteVlanInterface(vlanIfName string) error {
	// Check if vlan interface exists
	link, err := netlink.LinkByName(vlanIfName)
	if err != nil {
		return nil
	}
	// Delete the vlan interface
	err = netlink.LinkDel(link)
	if err != nil {
		return err
	}
	return nil
}

func getNodeTopology(provider string) ([]byte, error) {
	topology := vlanprovider.NodeTopology{
		Bonds:      make(map[string]vlanprovider.NicMap),
		SriovPools: make(map[string]vlanprovider.NicMap),
	}

	name2nic := make(map[string]vlanprovider.Nic)
	pci2nic := make(map[string]vlanprovider.Nic)
	bondIndex := make(map[string]int)
	bondIndex["tenant-bond"] = 0
	bondIndex["provider-bond"] = 0
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	ethHandle, err := ethtool.NewEthtool()
	if err != nil {
		return nil, err
	}
	defer ethHandle.Close()
	for _, link := range links {
		bondName := ""
		if link.Attrs().Name == "tenant-bond" {
			bondName = "tenant-bond"
		} else if link.Attrs().Name == "provider-bond" {
			bondName = "provider-bond"
		}
		if bondName != "" {
			bondIndex[bondName] = link.Attrs().Index
			topology.Bonds[bondName] = make(vlanprovider.NicMap)
		}
	}
	for _, link := range links {
		macAddress, err := ethHandle.PermAddr(link.Attrs().Name)
		if err != nil {
			return nil, err
		}
		if provider == "openstack" {
			if strings.HasPrefix(link.Attrs().Name, "eth") {
				pciAddress, err := ethHandle.BusInfo(link.Attrs().Name)
				if err != nil {
					return nil, err
				}
				pci2nic[pciAddress] = vlanprovider.Nic{
					Name:       link.Attrs().Name,
					MacAddress: macAddress}
			}
		}
		nic := vlanprovider.Nic{
			Name:       link.Attrs().Name,
			MacAddress: macAddress}
		name2nic[nic.Name] = nic
		bondName := ""
		if bondIndex["tenant-bond"] > 0 && link.Attrs().MasterIndex == bondIndex["tenant-bond"] {
			bondName = "tenant-bond"
		} else if bondIndex["provider-bond"] > 0 && link.Attrs().MasterIndex == bondIndex["provider-bond"] {
			bondName = "provider-bond"
		}
		if bondName != "" {
			var tmp []byte
			tmp, _ = json.Marshal(nic)
			var jsonNic vlanprovider.JsonNic
			json.Unmarshal(tmp, &jsonNic)
			if provider == "openstack" {
				topology.Bonds[bondName][nic.MacAddress] = jsonNic
			} else {
				topology.Bonds[bondName][nic.Name] = jsonNic
			}
		}
	}

	file, err := ioutil.ReadFile(sriovConfigFile)
	if err != nil {
		klog.Errorf("Error when getting sriovdp config file %s", sriovConfigFile)
	} else {
		var resourceList SriovResourceList
		err := json.Unmarshal(file, &resourceList)
		if err != nil {
			klog.Errorf("Error when reading sriovdp config file %s", sriovConfigFile)
		} else {
			for _, resource := range resourceList.Resources {
				topology.SriovPools[resource.ResourceName] = make(vlanprovider.NicMap)
				if provider == "openstack" {
					for _, pciAddress := range resource.Selectors.PCIAddresses {
						nic, ok := pci2nic[pciAddress]
						if ok {
							var tmp []byte
							tmp, _ = json.Marshal(nic)
							var jsonNic vlanprovider.JsonNic
							json.Unmarshal(tmp, &jsonNic)
							topology.SriovPools[resource.ResourceName][nic.MacAddress] = jsonNic
						}
					}
				} else {
					for _, pfName := range resource.Selectors.PFNames {
						nic, ok := name2nic[pfName]
						if ok {
							var tmp []byte
							tmp, _ = json.Marshal(nic)
							var jsonNic vlanprovider.JsonNic
							json.Unmarshal(tmp, &jsonNic)
							topology.SriovPools[resource.ResourceName][nic.Name] = jsonNic
						}
					}
				}
			}
		}
	}

	jsonTopology, err := json.Marshal(topology)
	return jsonTopology, err
}
